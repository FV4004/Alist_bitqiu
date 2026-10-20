package handles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/server/middlewares"
	"github.com/gin-gonic/gin"
	"github.com/xhofe/tache"
)

func testNASCompare(t *testing.T, source, dest, outside string, invoke func(gin.HandlerFunc, any, bool) map[string]json.RawMessage) {
	user := &model.User{ID: 1, BasePath: "/", Role: model.Roles{model.ADMIN}}
	write := func(base, name, contents string) {
		t.Helper()
		file := filepath.Join(base, "compare-fixture", name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"existing.txt": "cloud differs", "missing.txt": "missing payload", "missingFolder/child.dat": "child payload", "existingFolder/have.bin": "changed cloud", "existingFolder/need.bin": "needed", "rename_file.txt": "renamed", "rename_folder/file.dat": "review subtree", "episode02.mkv": "episode two", "conflict/hidden.bin": "blocked", "symlink/hidden.bin": "outside", "broken/fail-marker": "unreadable", "CaseDir/a.dat": "a", "casedir/b.dat": "b"} {
		write(source, name, content)
	}
	for name, content := range map[string]string{"existing.txt": "NAS original", "existingFolder/have.bin": "NAS unchanged", "rename-file.txt": "original renamed", "episode01.mkv": "episode one", "conflict": "keep file"} {
		write(dest, name, content)
	}
	for _, folder := range []string{"emptyDir"} {
		if err := os.MkdirAll(filepath.Join(source, "compare-fixture", folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dest, "compare-fixture", "rename-folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "compare-fixture", "symlink")); err != nil {
		t.Fatal(err)
	}
	d, err := op.GetStorageByMountPath("/cloud")
	if err != nil {
		t.Fatal(err)
	}
	fixture := d.(*nasFixtureDriver)
	oldHook := fixture.streamHook
	defer func() { fixture.streamHook = oldHook }()
	fixture.streamHook = func(ctx context.Context, objs []model.Obj, args model.ListArgs) ([]model.Obj, error) {
		for _, obj := range objs {
			if obj.GetName() == "fail-marker" {
				return nil, errors.New("fixture list failure")
			}
		}
		if args.OnPage != nil {
			if err := args.OnPage(objs); err != nil {
				return nil, err
			}
		}
		return objs, nil
	}
	initial := len(fs.NASDownloads())
	data, _ := json.Marshal(map[string]string{"src_dir": "/cloud/compare-fixture", "dst_dir": "/nas/compare-fixture"})
	w := httptestCompareContext(t, data, user, context.Background(), BitQiuNASCompare)
	if bytes.Contains(w.Body.Bytes(), []byte(source)) || bytes.Contains(w.Body.Bytes(), []byte(dest)) {
		t.Fatal("physical NAS paths exposed in scan response")
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/x-ndjson") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing streaming safety headers")
	}
	var done struct {
		Event  string            `json:"event"`
		ScanID string            `json:"scan_id"`
		Items  []nasCompareItem  `json:"items"`
		Issues []nasCompareIssue `json:"issues"`
	}
	dec := json.NewDecoder(w.Body)
	progress := 0
	for dec.More() {
		var frame json.RawMessage
		if err := dec.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		var kind struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(frame, &kind)
		if kind.Event == "progress" {
			progress++
		}
		if kind.Event == "done" {
			_ = json.Unmarshal(frame, &done)
		}
	}
	if done.ScanID == "" || progress == 0 {
		t.Fatal("incomplete stream", done, w.Body.String())
	}
	if len(fs.NASDownloads()) != initial {
		t.Fatal("scan queued downloads")
	}
	byPath := map[string]nasCompareItem{}
	for _, v := range done.Items {
		byPath[v.Relative] = v
	}
	for _, name := range []string{"existing.txt", "existingFolder/have.bin", "conflict/hidden.bin", "symlink/hidden.bin", "broken/fail-marker"} {
		if _, ok := byPath[name]; ok {
			t.Fatal("existing or blocked object classified missing", name)
		}
	}
	if byPath["rename_file.txt"].Category != "review" || byPath["rename_folder/file.dat"].Category != "review" || byPath["episode02.mkv"].Category != "files" {
		t.Fatal("unsafe fuzzy classification", byPath)
	}
	if !byPath["broken"].Blocked || len(done.Issues) < 3 {
		t.Fatal("incomplete folder not blocked", done.Issues)
	}
	code := func(r map[string]json.RawMessage) int { var n int; _ = json.Unmarshal(r["code"], &n); return n }
	prepare := func(names ...string) map[string]json.RawMessage {
		ids := []string{}
		for _, name := range names {
			ids = append(ids, byPath[name].ID)
		}
		return invoke(BitQiuNASComparePrepare, map[string]any{"scan_id": done.ScanID, "ids": ids}, true)
	}
	if code(prepare("broken")) == 200 {
		t.Fatal("accepted incomplete folder")
	}
	if code(prepare("CaseDir", "casedir")) == 200 {
		t.Fatal("accepted path aliases")
	}
	for _, handler := range []gin.HandlerFunc{BitQiuNASCompare, BitQiuNASComparePrepare, BitQiuNASCompareCommit} {
		protected := func(c *gin.Context) {
			middlewares.AuthAdmin(c)
			if !c.IsAborted() {
				handler(c)
			}
		}
		if code(invoke(protected, nil, false)) != 403 {
			t.Fatal("guest compare access")
		}
	}
	other := func(c *gin.Context) { copy := *user; copy.ID = 2; c.Set("user", &copy); BitQiuNASComparePrepare(c) }
	if code(invoke(other, map[string]any{"scan_id": done.ScanID, "ids": []string{byPath["missing.txt"].ID}}, true)) == 200 {
		t.Fatal("scan owner bypass")
	}
	// Existing queue targets and files created after scanning must be excluded before confirmation.
	obj, err := op.Get(context.Background(), d, "/compare-fixture/episode02.mkv")
	if err != nil {
		t.Fatal(err)
	}
	record, err := fs.NewNASRecord(user, "/cloud/compare-fixture/episode02.mkv", "/nas/compare-fixture/episode02.mkv", dest, "compare-fixture/episode02.mkv", "/cloud", "/nas", "/compare-fixture/episode02.mkv", obj)
	if err != nil {
		t.Fatal(err)
	}
	record.State = "paused"
	if err = fs.QueueNASDownloads([]fs.NASRecord{record}, user); err != nil {
		t.Fatal(err)
	}
	write(dest, "missing.txt", "external new file")
	result := prepare("missingFolder", "missingFolder/child.dat", "missingFolder/child.dat", "emptyDir", "missing.txt", "existingFolder/need.bin", "episode02.mkv")
	if code(result) != 200 {
		t.Fatal(string(result["message"]))
	}
	var plan struct {
		PlanID      string           `json:"plan_id"`
		Tasks       []nasCompareTask `json:"tasks"`
		Skipped     []nasCompareSkip `json:"skipped"`
		Files       int              `json:"files"`
		Directories int              `json:"directories"`
	}
	_ = json.Unmarshal(result["data"], &plan)
	if len(plan.Tasks) != 4 || len(plan.Skipped) != 2 || plan.Files != 2 || plan.Directories != 2 {
		t.Fatalf("bad dedup/skip preview %+v", plan)
	}
	if len(fs.NASDownloads()) != initial+1 {
		t.Fatal("preview queued downloads")
	}
	if code(invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.PlanID}, true)) == 200 {
		t.Fatal("accepted absent manual confirmation")
	}
	write(dest, "existingFolder/need.bin", "created during final review")
	committed := invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.PlanID, "confirmed": true}, true)
	if code(committed) != 200 {
		t.Fatal(committed)
	}
	var queued struct {
		Queued  int              `json:"queued"`
		Skipped []nasCompareSkip `json:"skipped"`
	}
	_ = json.Unmarshal(committed["data"], &queued)
	if queued.Queued != 3 || len(queued.Skipped) != 1 {
		t.Fatal("final race recheck", queued)
	}
	if code(invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.PlanID, "confirmed": true}, true)) == 200 {
		t.Fatal("plan replay")
	}
	executed := map[string]bool{}
	for {
		progressed := false
		for _, task := range fs.CopyTaskManager.GetAll() {
			view, ok := task.NASView()
			if !ok || !strings.HasPrefix(view.Source, "/cloud/compare-fixture/") || executed[task.GetID()] {
				continue
			}
			executed[task.GetID()] = true
			progressed = true
			worker := tache.Worker[*fs.CopyTask]{}
			worker.Execute(task)
			if task.GetState() != tache.StateSucceeded {
				t.Fatal(task.GetErr())
			}
		}
		if !progressed {
			break
		}
	}
	for file, want := range map[string]string{"missingFolder/child.dat": "child payload", "missing.txt": "external new file", "existing.txt": "NAS original", "existingFolder/need.bin": "created during final review", "rename-file.txt": "original renamed", "episode01.mkv": "episode one"} {
		got, err := os.ReadFile(filepath.Join(dest, "compare-fixture", file))
		if err != nil || string(got) != want {
			t.Fatalf("data overwritten/missing %s %q %v", file, got, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "compare-fixture", "emptyDir")); err != nil || !info.IsDir() {
		t.Fatal("empty folder not created", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	w = httptestCompareContext(t, data, user, canceled, BitQiuNASCompare)
	if strings.Contains(w.Body.String(), `"event":"done"`) {
		t.Fatal("canceled scan completed")
	}
	for _, pair := range [][2]string{{"episode01.mkv", "episode02.mkv"}, {"movie1.mkv", "movie2.mkv"}} {
		if compareSimilar(pair[0], pair[1]) {
			t.Fatal("episode mismatch matched", pair)
		}
	}
	if !compareSimilar("rename_file.txt", "rename-file.txt") {
		t.Fatal("missing rename candidate")
	}
}

func httptestCompareContext(t *testing.T, data []byte, user *model.User, ctx context.Context, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(data)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	c.Set("meta", (*model.Meta)(nil))
	handler(c)
	return w
}
