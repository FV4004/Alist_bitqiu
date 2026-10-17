package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/drivers/local"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/db"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/xhofe/tache"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type nasHTTPFixture struct {
	local.Local
	endpoint string
}

func (*nasHTTPFixture) Config() driver.Config {
	return driver.Config{Name: "NASHTTPFixture", LocalSort: true}
}
func (d *nasHTTPFixture) Link(_ context.Context, obj model.Obj, _ model.LinkArgs) (*model.Link, error) {
	return &model.Link{URL: d.endpoint + "/" + obj.GetName()}, nil
}
func nasEventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("download state did not settle")
}

// Real HTTP requests and the original one-worker queue verify cancellation,
// safe byte-range resume and that batch starts cannot bypass concurrency limits.
func TestNASDownloadLifecycle(t *testing.T) {
	oldConf, oldManager := conf.Conf, CopyTaskManager
	conf.Conf = conf.DefaultConfig()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tasks.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(database)
	ResetNASDownloads()
	if err = InitNASDownloads(); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("NAS-range-fixture-"), 65536)
	limitRelease := make(chan struct{})
	var active, maxActive atomic.Int32
	var offsetsMu sync.Mutex
	offsets := map[string][]int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if count <= old || maxActive.CompareAndSwap(old, count) {
				break
			}
		}
		if r.URL.Path == "/failure" {
			http.Error(w, "fixture failure", 503)
			return
		}
		offset := int64(0)
		if value := r.Header.Get("Range"); value != "" && r.URL.Path != "/no-range" {
			fmt.Sscanf(value, "bytes=%d-", &offset)
		}
		offsetsMu.Lock()
		offsets[r.URL.Path] = append(offsets[r.URL.Path], offset)
		offsetsMu.Unlock()
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-int(offset)))
		w.Header().Set("ETag", `"fixture-v1"`)
		if offset > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(payload)-1, len(payload)))
			w.WriteHeader(206)
		}
		for start := int(offset); start < len(payload); start += 4096 {
			end := min(start+4096, len(payload))
			if _, err := w.Write(payload[start:end]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if strings.HasPrefix(r.URL.Path, "/limit-") && start == int(offset) {
				select {
				case <-limitRelease:
				case <-r.Context().Done():
					return
				}
			}
			if end == len(payload) {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}))
	defer server.Close()
	op.RegisterDriver(func() driver.Driver { return &nasHTTPFixture{endpoint: server.URL} })
	source, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{"replace", "changed", "resume", "no-range", "cancel", "queue1", "queue2", "failure", "limit-0", "limit-1", "limit-2", "limit-3", "limit-4", "limit-5", "limit-6", "limit-7"} {
		if err = os.WriteFile(filepath.Join(source, name), payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, cfg := range []struct{ driver, mount, root string }{{"NASHTTPFixture", "/nas-http-source", source}, {"Local", "/nas-http-dest", dest}} {
		addition, _ := json.Marshal(map[string]any{"root_folder_path": cfg.root, "mkdir_perm": "755"})
		if _, err = op.CreateStorage(context.Background(), model.Storage{Driver: cfg.driver, MountPath: cfg.mount, Addition: string(addition)}); err != nil {
			t.Fatal(err)
		}
	}
	CopyTaskManager = tache.NewManager[*CopyTask](tache.WithWorks(1), tache.WithMaxRetry(0))
	user := &model.User{ID: 1}
	makeTask := func(name string) NASRecord {
		t.Helper()
		d, _ := op.GetStorageByMountPath("/nas-http-source")
		obj, e := op.Get(context.Background(), d, "/"+name)
		if e != nil {
			t.Fatal(e)
		}
		r, e := NewNASRecord(user, "/nas-http-source/"+name, "/nas-http-dest/"+name, dest, name, "/nas-http-source", "/nas-http-dest", "/"+name, obj)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	view := func(id string) NASView {
		n, ok := nasFind(id)
		if !ok {
			t.Fatal("lost task", id)
		}
		return n.view()
	}
	control := func(id, action string, restart bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ok, e := ControlNASDownload(ctx, id, action, restart, user)
		if e != nil || !ok {
			t.Fatalf("%s: applied=%v err=%v", action, ok, e)
		}
	}
	t.Cleanup(func() {
		for _, v := range NASDownloads() {
			if v.CanCancel {
				_, _ = ControlNASDownload(context.Background(), v.ID, "cancel", false, user)
			}
		}
		nasEventually(t, func() bool { return active.Load() == 0 })
		for _, d := range op.GetAllStorages() {
			if strings.HasPrefix(d.GetStorage().MountPath, "/nas-http-") {
				_ = op.DisableStorage(context.Background(), d.GetStorage().ID)
			}
		}
		ResetNASDownloads()
		CopyTaskManager = oldManager
		sql, _ := database.DB()
		_ = sql.Close()
		conf.Conf = oldConf
	})
	t.Run("pause preserves bytes and resumes without repeating prefix", func(t *testing.T) {
		r := makeTask("resume")
		if err = QueueNASDownloads([]NASRecord{r}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(r.ID).TransferredBytes >= 32768 })
		control(r.ID, "pause", false)
		paused := view(r.ID)
		if paused.State != "paused" || paused.TransferredBytes <= 0 || paused.TransferredBytes >= int64(len(payload)) {
			t.Fatalf("bad pause: %+v", paused)
		}
		partial, e := os.Stat(filepath.Join(dest, nasPart(r)))
		if e != nil || partial.Size() != paused.TransferredBytes {
			t.Fatalf("lost partial: %v", e)
		}
		control(r.ID, "start", false)
		nasEventually(t, func() bool { return view(r.ID).State == "completed" })
		result, e := os.ReadFile(filepath.Join(dest, r.Relative))
		if e != nil || !bytes.Equal(result, payload) {
			t.Fatal("resume corrupted file", e)
		}
		offsetsMu.Lock()
		got := append([]int64(nil), offsets["/resume"]...)
		offsetsMu.Unlock()
		if len(got) != 2 || got[1] != paused.TransferredBytes {
			t.Fatalf("resume repeated data: %v paused=%d", got, paused.TransferredBytes)
		}
	})
	t.Run("source without range requires explicit restart", func(t *testing.T) {
		r := makeTask("no-range")
		if err = QueueNASDownloads([]NASRecord{r}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(r.ID).TransferredBytes >= 32768 })
		control(r.ID, "pause", false)
		control(r.ID, "start", false)
		nasEventually(t, func() bool { return view(r.ID).RestartRequired })
		if view(r.ID).State != "paused" {
			t.Fatal("unsupported resume not paused")
		}
		if _, e := ControlNASDownload(context.Background(), r.ID, "start", false, user); e == nil {
			t.Fatal("silent restart allowed")
		}
		control(r.ID, "start", true)
		nasEventually(t, func() bool { return view(r.ID).State == "completed" })
		result, _ := os.ReadFile(filepath.Join(dest, r.Relative))
		if !bytes.Equal(result, payload) {
			t.Fatal("restart corrupted file")
		}
	})
	t.Run("cancel stops backend and removes only its partial", func(t *testing.T) {
		r := makeTask("cancel")
		if err = QueueNASDownloads([]NASRecord{r}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(r.ID).TransferredBytes >= 32768 })
		control(r.ID, "cancel", false)
		if view(r.ID).State != "canceled" || view(r.ID).CompletionReason != "user_canceled" {
			t.Fatal("cancel state wrong")
		}
		nasEventually(t, func() bool { return active.Load() == 0 })
		if _, e := os.Stat(filepath.Join(dest, nasPart(r))); !os.IsNotExist(e) {
			t.Fatal("canceled partial remains", e)
		}
		if _, e := os.Stat(filepath.Join(dest, r.Relative)); !os.IsNotExist(e) {
			t.Fatal("cancel published incomplete final", e)
		}
		if data, e := os.ReadFile(filepath.Join(dest, "resume")); e != nil || !bytes.Equal(data, payload) {
			t.Fatal("cancel affected another task")
		}
	})
	t.Run("all pause freezes waiting queue and starts obey worker limit", func(t *testing.T) {
		nasEventually(t, func() bool { return active.Load() == 0 })
		maxActive.Store(0)
		a, b := makeTask("queue1"), makeTask("queue2")
		if err = QueueNASDownloads([]NASRecord{a, b}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(a.ID).TransferredBytes >= 32768 })
		paused, e := PauseNASWaiting()
		if e != nil || !paused[b.ID] {
			t.Fatal("waiting task not paused", e)
		}
		control(a.ID, "pause", false)
		nasEventually(t, func() bool { return active.Load() == 0 })
		if view(b.ID).State != "paused" || view(b.ID).TransferredBytes != 0 {
			t.Fatal("waiting queue started during all-pause")
		}
		control(a.ID, "start", false)
		control(b.ID, "start", false)
		nasEventually(t, func() bool { return view(a.ID).State == "completed" && view(b.ID).State == "completed" })
		if maxActive.Load() > 1 {
			t.Fatalf("worker limit bypassed: %d", maxActive.Load())
		}
	})
	t.Run("limit 5 and shrinking pauses latest starts while retaining oldest", func(t *testing.T) {
		// Release fixture sources even when an assertion fails; parent cleanup must
		// not block inside httptest.Server.Close while reporting the failure.
		defer func() {
			select {
			case <-limitRelease:
			default:
				close(limitRelease)
			}
		}()
		nasEventually(t, func() bool { return active.Load() == 0 })
		maxActive.Store(0)
		CopyTaskManager.SetWorkersNumActive(8) // The NAS cap must win over a larger shared queue.
		for _, bad := range []int{0, 6, -1} {
			if _, e := SetNASConcurrency(context.Background(), bad); e == nil {
				t.Fatalf("accepted limit %d", bad)
			}
		}
		records := []NASRecord{}
		for i := 0; i < 8; i++ {
			records = append(records, makeTask(fmt.Sprintf("limit-%d", i)))
		}
		for i := 0; i < 5; i++ {
			if e := QueueNASDownloads(records[i:i+1], user); e != nil {
				t.Fatal(e)
			}
			id := records[i].ID
			nasEventually(t, func() bool { return view(id).State == "downloading" && view(id).TransferredBytes >= 4096 })
		}
		var wg sync.WaitGroup
		for i := 5; i < 8; i++ {
			wg.Add(1)
			go func(r NASRecord) {
				defer wg.Done()
				if e := QueueNASDownloads([]NASRecord{r}, user); e != nil {
					t.Error(e)
				}
			}(records[i])
		}
		wg.Wait()
		if v := NASConcurrencyStatus(); v.Running != 5 || v.Limit != 5 {
			t.Fatalf("bad initial concurrency: %+v", v)
		}
		for _, r := range records[5:] {
			if view(r.ID).State != "waiting" || view(r.ID).TransferredBytes != 0 {
				t.Fatal("queued task bypassed cap")
			}
		}
		result, e := SetNASConcurrency(context.Background(), 2)
		if e != nil || result.Paused != 3 || result.Running != 2 {
			t.Fatalf("shrink: %+v, %v", result, e)
		}
		nasEventually(t, func() bool { return active.Load() == 2 })
		for i, r := range records[:5] {
			v := view(r.ID)
			if i < 2 && v.State != "downloading" {
				t.Fatal("paused an older task")
			}
			if i >= 2 {
				if v.State != "paused" || v.TransferredBytes < 4096 {
					t.Fatalf("latest task lost pause/bytes: %+v", v)
				}
				info, e := os.Stat(filepath.Join(dest, nasPart(r)))
				if e != nil || info.Size() != v.TransferredBytes {
					t.Fatal("partial lost", e)
				}
			}
		}
		if saved, e := loadNASConcurrency(); e != nil || saved != 2 {
			t.Fatal("limit not persisted", e)
		}
		if _, e = SetNASConcurrency(context.Background(), 5); e != nil {
			t.Fatal(e)
		}
		nasEventually(t, func() bool { return NASConcurrencyStatus().Running == 5 })
		for _, r := range records[2:5] {
			if view(r.ID).State != "paused" {
				t.Fatal("raising limit silently resumed a paused task")
			}
		}
		close(limitRelease)
		nasEventually(t, func() bool {
			for _, i := range []int{0, 1, 5, 6, 7} {
				if view(records[i].ID).State != "completed" {
					return false
				}
			}
			return true
		})
		for _, r := range records[2:5] {
			control(r.ID, "start", false)
		}
		nasEventually(t, func() bool {
			for _, r := range records {
				if view(r.ID).State != "completed" {
					return false
				}
			}
			return true
		})
		for _, r := range records {
			data, e := os.ReadFile(filepath.Join(dest, r.Relative))
			if e != nil || !bytes.Equal(data, payload) {
				t.Fatal("limited resume corrupted file", e)
			}
		}
		if maxActive.Load() > 5 {
			t.Fatalf("cap bypassed with 8 shared workers: %d", maxActive.Load())
		}
		if _, e = SetNASConcurrency(context.Background(), 1); e != nil {
			t.Fatal(e)
		}
		CopyTaskManager.SetWorkersNumActive(1)
	})
	t.Run("HTTP failure persists and permits retry", func(t *testing.T) {
		r := makeTask("failure")
		if err = QueueNASDownloads([]NASRecord{r}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(r.ID).State == "failed" })
		v := view(r.ID)
		if !v.CanStart || !strings.Contains(v.Error, "503") {
			t.Fatalf("failure not actionable: %+v", v)
		}
	})

	t.Run("confirmed replacement preserves old data until atomic commit", func(t *testing.T) {
		r := makeTask("replace")
		old := []byte("original NAS content")
		if err := os.WriteFile(filepath.Join(dest, r.Relative), old, 0600); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dest)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		r.TargetVersion, _, err = NASPathVersion(root, r.Relative)
		if err != nil {
			t.Fatal(err)
		}
		r.Replace = true
		if err := QueueNASDownloads([]NASRecord{r}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(r.ID).TransferredBytes >= 32768 })
		data, _ := os.ReadFile(filepath.Join(dest, r.Relative))
		if !bytes.Equal(data, old) {
			t.Fatal("old file replaced before full transfer")
		}
		nasEventually(t, func() bool { return view(r.ID).State == "completed" })
		data, _ = os.ReadFile(filepath.Join(dest, r.Relative))
		if !bytes.Equal(data, payload) {
			t.Fatal("replacement incomplete")
		}
		changed := makeTask("changed")
		if err := os.WriteFile(filepath.Join(dest, changed.Relative), old, 0600); err != nil {
			t.Fatal(err)
		}
		changed.TargetVersion, _, _ = NASPathVersion(root, changed.Relative)
		changed.Replace = true
		if err := os.WriteFile(filepath.Join(dest, changed.Relative), []byte("newer NAS data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := QueueNASDownloads([]NASRecord{changed}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(changed.ID).State == "failed" })
		data, _ = os.ReadFile(filepath.Join(dest, changed.Relative))
		if string(data) != "newer NAS data" {
			t.Fatal("changed destination overwritten")
		}
		failure := makeTask("failure")
		failure.ID = "replace-failure"
		failure.Relative = "replace-failure"
		failure.Destination += "-replacement"
		if err := os.WriteFile(filepath.Join(dest, failure.Relative), old, 0600); err != nil {
			t.Fatal(err)
		}
		failure.TargetVersion, _, _ = NASPathVersion(root, failure.Relative)
		failure.Replace = true
		if err := QueueNASDownloads([]NASRecord{failure}, user); err != nil {
			t.Fatal(err)
		}
		nasEventually(t, func() bool { return view(failure.ID).State == "failed" })
		data, _ = os.ReadFile(filepath.Join(dest, failure.Relative))
		if !bytes.Equal(data, old) {
			t.Fatal("failed download damaged original")
		}
	})
	t.Run("history cleanup retention whole groups and durable rollback", func(t *testing.T) {
		past := time.Now().Add(-91 * 24 * time.Hour)
		recent := time.Now().Add(-time.Hour)
		fixtures := []NASRecord{{ID: "hist-a", GroupID: "hist-group", State: "completed", EndTime: &past}, {ID: "hist-b", GroupID: "hist-group", State: "failed", EndTime: &recent}, {ID: "hist-old", State: "failed", EndTime: &past}, {ID: "hist-wait", State: "waiting"}, {ID: "hist-paused", State: "paused"}, {ID: "hist-mixed-done", GroupID: "mixed", State: "completed", EndTime: &past}, {ID: "hist-mixed-pause", GroupID: "mixed", State: "paused"}}
		nasRegistry.Lock()
		for _, r := range fixtures {
			nasRegistry.tasks[r.ID] = &nasTask{record: r}
		}
		nasRegistry.Unlock()
		if err := SaveNASDownloads(); err != nil {
			t.Fatal(err)
		}
		result, err := ClearNASHistory(nil, true, false, time.Now().Add(-90*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := nasFind("hist-old"); ok || result.Deleted < 1 {
			t.Fatal("expired record retained")
		}
		for _, id := range []string{"hist-a", "hist-b", "hist-wait", "hist-paused", "hist-mixed-done"} {
			if _, ok := nasFind(id); !ok {
				t.Fatal("pruned active/recent group", id)
			}
		}
		result, err = ClearNASHistory([]string{"hist-a"}, false, false, time.Time{})
		if err != nil || result.Deleted != 2 {
			t.Fatalf("group not cleared: %+v %v", result, err)
		}
		result, err = ClearNASHistory([]string{"mixed"}, false, false, time.Time{})
		if err != nil || result.Deleted != 0 {
			t.Fatal("cleared incomplete group")
		}
		// Keep generated fixture tasks out of the following persistence test.
		nasRegistry.Lock()
		for _, r := range fixtures {
			delete(nasRegistry.tasks, r.ID)
		}
		nasRegistry.Unlock()
		if err := SaveNASDownloads(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("journal restores history and pauses interrupted tasks", func(t *testing.T) {
		nasEventually(t, func() bool {
			for _, v := range CopyTaskManager.GetAll() {
				if v.GetState() == tache.StateRunning {
					return false
				}
			}
			return true
		})
		n := &nasTask{record: NASRecord{ID: "interrupted", State: "downloading", TotalBytes: 100, TransferredBytes: 40}}
		nasRegistry.Lock()
		nasRegistry.tasks["interrupted"] = n
		nasRegistry.Unlock()
		if err = SaveNASDownloads(); err != nil {
			t.Fatal(err)
		}
		count := len(NASDownloads())
		ResetNASDownloads()
		if err = InitNASDownloads(); err != nil {
			t.Fatal(err)
		}
		if NASConcurrencyStatus().Limit != 1 {
			t.Fatal("concurrency setting lost on restart")
		}
		if len(NASDownloads()) != count || view("interrupted").State != "paused" || view("interrupted").TransferredBytes != 40 {
			t.Fatal("journal recovery lost state")
		}
		nasRegistry.Lock()
		delete(nasRegistry.tasks, "interrupted")
		nasRegistry.Unlock()
	})
	t.Run("corrupt journal cannot be overwritten", func(t *testing.T) {
		if e := database.Model(&model.TaskItem{}).Where("key = ?", nasJournalKey).Update("persist_data", "not JSON").Error; e != nil {
			t.Fatal(e)
		}
		if InitNASDownloads() == nil || SaveNASDownloads() == nil {
			t.Fatal("corrupt journal was overwritten")
		}
		var saved model.TaskItem
		database.Where("key = ?", nasJournalKey).First(&saved)
		if saved.PersistData != "not JSON" {
			t.Fatal("original journal destroyed")
		}
		ResetNASDownloads()
	})
}

func TestNASHTTPResumeGuard(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  string
		etag    string
		restart bool
	}{{"ignored range", 200, "", `"v1"`, true}, {"wrong offset", 206, "bytes 0-99/100", `"v1"`, true}, {"changed size", 206, "bytes 10-109/110", `"v1"`, true}, {"changed entity", 206, "bytes 10-99/100", `"v2"`, true}, {"valid", 206, "bytes 10-99/100", `"v1"`, false}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=10-" || r.Header.Get("If-Range") != `"v1"` {
					t.Error("missing resume headers")
				}
				w.Header().Set("Content-Range", test.header)
				w.Header().Set("ETag", test.etag)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, strings.Repeat("x", 90))
			}))
			defer s.Close()
			reader, total, _, _, err := openNASSource(context.Background(), &model.Link{URL: s.URL}, 10, NASRecord{TotalBytes: 100, Validator: `"v1"`, ValidatorKind: "ETag"})
			if reader != nil {
				reader.Close()
			}
			if test.restart && !errors.Is(err, errNASRestart) {
				t.Fatalf("unsafe resume accepted: %v", err)
			}
			if !test.restart && (err != nil || total != 100) {
				t.Fatalf("valid range rejected: %v total=%d", err, total)
			}
		})
	}
}
