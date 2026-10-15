package handles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/drivers/local"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/db"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/server/middlewares"
	"github.com/gin-gonic/gin"
	"github.com/xhofe/tache"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Local fixtures exercise the real copy queue and Local.Put without an account
// or any dependency on the live BitQiu service.
type nasFixtureDriver struct{ local.Local }

func (*nasFixtureDriver) Config() driver.Config {
	return driver.Config{Name: "BitQiu", LocalSort: true}
}

func TestBitQiuNASIntegration(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "nas.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(database)
	fs.ResetNASDownloads()
	if err := fs.InitNASDownloads(); err != nil {
		t.Fatal(err)
	}
	op.ClearRoleCache()
	for _, role := range []model.Role{
		{ID: uint(model.ADMIN), Name: "admin", PermissionScopes: []model.PermissionEntry{{Path: "/", Permission: 65535}}},
		{ID: uint(model.GUEST), Name: "guest", PermissionScopes: []model.PermissionEntry{{Path: "/", Permission: 0}}},
	} {
		if err := op.CreateRole(&role); err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, _ := database.DB()
	originalDriver, _ := op.GetDriver("BitQiu")
	op.RegisterDriver(func() driver.Driver { return &nasFixtureDriver{} })
	oldManager := fs.CopyTaskManager
	fs.CopyTaskManager = tache.NewManager[*fs.CopyTask](tache.WithWorks(1), tache.WithMaxRetry(0), func(o *tache.Options) { o.Running = false })
	t.Cleanup(func() {
		for _, d := range op.GetAllStorages() {
			_ = op.DisableStorage(context.Background(), d.GetStorage().ID)
		}
		fs.ResetNASDownloads()
		fs.CopyTaskManager = oldManager
		if originalDriver != nil {
			op.RegisterDriver(originalDriver)
		}
		op.ClearRoleCache()
		op.SettingCacheUpdate()
		sqlDB.Close()
		conf.Conf = oldConf
	})
	source, dest, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for i := 0; i < 237; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("file-%03d.txt", i)), []byte("NAS download fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(source, "folder", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "folder", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "folder", "nested", "child.txt"), []byte("nested fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 237; i++ {
		at := stamp.Add(time.Duration(i+1) * time.Minute)
		if err := os.Chtimes(filepath.Join(source, fmt.Sprintf("file-%03d.txt", i)), at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(source, "folder"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []struct{ driver, mount, root string }{{"BitQiu", "/cloud", source}, {"Local", "/nas", dest}} {
		addition, _ := json.Marshal(map[string]any{"root_folder_path": cfg.root, "show_hidden": true, "mkdir_perm": "755"})
		if _, err := op.CreateStorage(context.Background(), model.Storage{Driver: cfg.driver, MountPath: cfg.mount, Addition: string(addition)}); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(handler gin.HandlerFunc, body any, admin bool) map[string]json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(data))
		c.Request.Header.Set("Content-Type", "application/json")
		user := &model.User{ID: 1, BasePath: "/", Role: model.Roles{2}}
		if !admin {
			user.Role = model.Roles{model.GUEST}
		}
		c.Set("user", user)
		c.Set("meta", (*model.Meta)(nil))
		handler(c)
		var result map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("invalid response %s: %v", w.Body.String(), err)
		}
		return result
	}
	code := func(result map[string]json.RawMessage) int {
		var n int
		_ = json.Unmarshal(result["code"], &n)
		return n
	}
	requireOK := func(t *testing.T, result map[string]json.RawMessage) {
		t.Helper()
		if code(result) != 200 {
			t.Fatalf("request failed: %s", result)
		}
	}
	t.Run("complete browser pagination", func(t *testing.T) {
		seen := map[string]bool{}
		for page := 1; page <= 3; page++ {
			result := invoke(FsList, map[string]any{"path": "/cloud", "page": page, "per_page": 100}, true)
			requireOK(t, result)
			var data FsListResp
			if err := json.Unmarshal(result["data"], &data); err != nil {
				t.Fatal(err)
			}
			if data.Total != 238 || data.HasMore != (page < 3) {
				t.Fatalf("bad totals/paging: %+v", data)
			}
			for _, obj := range data.Content {
				if seen[obj.Name] {
					t.Fatal("duplicate entry", obj.Name)
				}
				seen[obj.Name] = true
			}
		}
		if len(seen) != 238 {
			t.Fatalf("lost entries: %d", len(seen))
		}
		result := invoke(FsList, map[string]any{"path": "/cloud"}, true)
		requireOK(t, result)
		var data FsListResp
		_ = json.Unmarshal(result["data"], &data)
		if len(data.Content) != 238 || data.HasMore {
			t.Fatalf("unpaged browser lost entries: %d", len(data.Content))
		}
	})
	t.Run("sort whole directory before pagination", func(t *testing.T) {
		for _, page := range []int{1, 3} {
			result := invoke(FsList, map[string]any{"path": "/cloud", "page": page, "per_page": 100, "order_by": "modified", "order_direction": "desc"}, true)
			requireOK(t, result)
			var data FsListResp
			_ = json.Unmarshal(result["data"], &data)
			want := fmt.Sprintf("file-%03d.txt", 237-1-(page-1)*100)
			if data.Content[0].Name != want {
				t.Fatalf("page %d: %s != %s", page, data.Content[0].Name, want)
			}
		}
		result := invoke(FsList, map[string]any{"path": "/cloud", "page": 1, "per_page": 100, "order_by": "modified", "order_direction": "asc"}, true)
		requireOK(t, result)
		var data FsListResp
		_ = json.Unmarshal(result["data"], &data)
		if data.Content[0].Name != "folder" {
			t.Fatal("oldest-first order ignored")
		}
		if code(invoke(FsList, map[string]any{"path": "/cloud", "order_by": "invalid"}, true)) == 200 {
			t.Fatal("accepted invalid sort")
		}
	})
	t.Run("visual directories and confinement", func(t *testing.T) {
		requireOK(t, invoke(BitQiuNASMkdir, map[string]string{"path": "/nas", "name": "Movies"}, true))
		result := invoke(BitQiuNASDirs, map[string]string{"path": "/nas"}, true)
		requireOK(t, result)
		var dirs []nasTarget
		_ = json.Unmarshal(result["data"], &dirs)
		if len(dirs) != 1 || dirs[0].Path != "Movies" {
			t.Fatalf("bad folder choices: %+v", dirs)
		}
		if err := os.Symlink(outside, filepath.Join(dest, "escape")); err != nil {
			t.Fatal(err)
		}
		if code(invoke(BitQiuNASDirs, map[string]string{"path": "/nas/escape"}, true)) == 200 {
			t.Fatal("followed symlink outside mount")
		}
		if code(invoke(BitQiuNASMkdir, map[string]string{"path": "/nas", "name": "../outside"}, true)) == 200 {
			t.Fatal("allowed traversal")
		}
		if code(invoke(BitQiuNASDirs, map[string]string{"path": "/cloud"}, true)) == 200 {
			t.Fatal("accepted non-Local destination")
		}
	})
	t.Run("background file and recursive downloads", func(t *testing.T) {
		result := invoke(BitQiuNASDownload, MoveCopyReq{SrcDir: "/cloud", DstDir: "/nas/Movies", Names: []string{"file-000.txt", "folder"}}, true)
		requireOK(t, result)
		// Drain queued work deterministically: the upstream task fields are not
		// synchronized for concurrent reads, so this test owns execution.
		executed := map[string]bool{}
		for {
			progressed := false
			for _, task := range fs.CopyTaskManager.GetAll() {
				if executed[task.GetID()] {
					continue
				}
				executed[task.GetID()] = true
				progressed = true
				worker := tache.Worker[*fs.CopyTask]{}
				worker.Execute(task)
				if task.GetState() != tache.StateSucceeded {
					t.Fatalf("copy failed: %v", task.GetErr())
				}
			}
			if !progressed {
				break
			}
		}
		var transferred int64
		for _, task := range fs.CopyTaskManager.GetAll() {
			snapshot, ok := task.NASView()
			if !ok {
				t.Fatal("missing NAS snapshot")
			}
			transferred += snapshot.TransferredBytes
			if snapshot.CompletionReason != "completed" {
				t.Fatalf("bad completed outcome: %+v", snapshot)
			}
		}
		if transferred != int64(len("NAS download fixture")+len("nested fixture")) {
			t.Fatalf("wrong real stream bytes: %d", transferred)
		}
		for name, want := range map[string]string{"file-000.txt": "NAS download fixture", "folder/nested/child.txt": "nested fixture"} {
			data, err := os.ReadFile(filepath.Join(dest, "Movies", name))
			if err != nil || string(data) != want {
				t.Fatalf("download %s: %q, %v", name, data, err)
			}
		}
		if info, err := os.Stat(filepath.Join(dest, "Movies", "folder", "empty")); err != nil || !info.IsDir() {
			t.Fatal("empty folder missing", err)
		}
		if code(invoke(BitQiuNASDownload, MoveCopyReq{SrcDir: "/cloud", DstDir: "/nas/Movies", Names: []string{"file-000.txt"}}, true)) == 200 {
			t.Fatal("accepted existing destination")
		}
	})
	t.Run("recursive size de-duplicates selected folders", func(t *testing.T) {
		result := invoke(BitQiuNASStats, nasSelection{SrcDir: "/cloud", Names: []string{"folder", "file-000.txt", "folder"}}, true)
		requireOK(t, result)
		var stats nasSelectionStats
		if err := json.Unmarshal(result["data"], &stats); err != nil {
			t.Fatal(err)
		}
		if stats.SelectedCount != 2 || stats.FileCount != 2 || stats.KnownBytes != int64(len("nested fixture")+len("NAS download fixture")) || stats.UnknownCount != 0 {
			t.Fatalf("wrong recursive total: %+v", stats)
		}
	})
	t.Run("batch scope confirmation counts and waiting queue pause", func(t *testing.T) {
		result := invoke(BitQiuNASDownload, MoveCopyReq{SrcDir: "/cloud", DstDir: "/nas/Movies", Names: []string{"file-003.txt", "file-004.txt"}}, true)
		requireOK(t, result)
		var response struct {
			Tasks []struct {
				ID string `json:"id"`
			}
		}
		_ = json.Unmarshal(result["data"], &response)
		ids := []string{response.Tasks[0].ID, response.Tasks[1].ID}
		result = invoke(BitQiuNASControl, map[string]any{"scope": "selected", "ids": ids[:1], "action": "pause"}, true)
		requireOK(t, result)
		var counts struct{ Success, Failed, Skipped int }
		_ = json.Unmarshal(result["data"], &counts)
		if counts.Success != 0 || counts.Skipped != 1 {
			t.Fatal("selected pause changed waiting task")
		}
		result = invoke(BitQiuNASControl, map[string]any{"scope": "all", "action": "pause"}, true)
		requireOK(t, result)
		_ = json.Unmarshal(result["data"], &counts)
		if counts.Success != 2 {
			t.Fatalf("all-pause missed waiting tasks: %+v", counts)
		}
		if code(invoke(BitQiuNASControl, map[string]any{"scope": "all", "action": "cancel"}, true)) == 200 {
			t.Fatal("cancel without confirmation allowed")
		}
		result = invoke(BitQiuNASControl, map[string]any{"scope": "selected", "ids": []string{ids[0], "missing"}, "action": "cancel", "confirmed": true}, true)
		requireOK(t, result)
		_ = json.Unmarshal(result["data"], &counts)
		if counts.Success != 1 || counts.Failed != 1 {
			t.Fatalf("wrong counts: %+v", counts)
		}
		for _, v := range fs.NASDownloads() {
			if v.ID == ids[1] && v.State != "paused" {
				t.Fatal("selected cancel changed another task")
			}
		}
		result = invoke(BitQiuNASTasks, nil, true)
		requireOK(t, result)
		if bytes.Contains(result["data"], []byte(dest)) || bytes.Contains(result["data"], []byte(source)) {
			t.Fatal("physical path leaked")
		}
		result = invoke(BitQiuNASControl, map[string]any{"scope": "all", "action": "cancel", "confirmed": true}, true)
		requireOK(t, result)
	})
	t.Run("validation before queueing", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(dest, "file-001.txt")); err != nil {
			t.Fatal(err)
		}
		for _, req := range []MoveCopyReq{
			{SrcDir: "/nas", DstDir: "/nas/Movies", Names: []string{"Movies"}},
			{SrcDir: "/cloud", DstDir: "/cloud", Names: []string{"file-000.txt"}},
			{SrcDir: "/cloud", DstDir: "/nas", Names: []string{"../escape"}},
			{SrcDir: "/cloud", DstDir: "/nas", Names: []string{"file-001.txt"}},
			{SrcDir: "/cloud", DstDir: "/nas", Names: []string{"file-002.txt", "file-002.txt"}},
			{SrcDir: "/cloud", DstDir: "/nas", Names: []string{"file-002.txt"}, Overwrite: true},
		} {
			if code(invoke(BitQiuNASDownload, req, true)) == 200 {
				t.Fatalf("accepted invalid download: %+v", req)
			}
		}
	})
	t.Run("targets omit credentials and admin guard", func(t *testing.T) {
		result := invoke(BitQiuNASTargets, nil, true)
		requireOK(t, result)
		var data struct{ Cloud, Targets []nasTarget }
		_ = json.Unmarshal(result["data"], &data)
		if len(data.Cloud) != 1 || len(data.Targets) != 1 || bytes.Contains(result["data"], []byte(source)) {
			t.Fatal("invalid/unsafe mount response")
		}
		protected := func(c *gin.Context) {
			middlewares.AuthAdmin(c)
			if !c.IsAborted() {
				BitQiuNASTargets(c)
			}
		}
		if code(invoke(protected, nil, false)) == 200 {
			t.Fatal("non-admin allowed NAS API")
		}
	})
	t.Run("enable requires existing mount and is idempotent", func(t *testing.T) {
		if err := enableNASDownloads(context.Background(), filepath.Join(dest, "missing"), "/enabled"); err == nil {
			t.Fatal("created absent mount")
		}
		if err := enableNASDownloads(context.Background(), dest, "/enabled"); err != nil {
			t.Fatal(err)
		}
		if err := enableNASDownloads(context.Background(), dest, "/enabled"); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(filepath.Join(dest, "Movies")); err != nil || !info.IsDir() {
			t.Fatal("fixture target missing", err)
		}
		if code(invoke(FsList, map[string]any{"path": "/enabled"}, false)) == 200 {
			t.Fatal("new NAS mount exposed to guest")
		}
		requireOK(t, invoke(FsList, map[string]any{"path": "/enabled"}, true))
		if err := enableNASDownloads(context.Background(), source, "/enabled"); err == nil {
			t.Fatal("replaced conflicting mount")
		}
	})
}
