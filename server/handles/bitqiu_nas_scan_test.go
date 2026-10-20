package handles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/server/middlewares"
	"github.com/gin-gonic/gin"
)

func testNASBackgroundScan(t *testing.T, source, dest string, invoke func(gin.HandlerFunc, any, bool) map[string]json.RawMessage) {
	for _, name := range []string{"chosen/child.txt", "single.bin", "unselected/secret.txt"} {
		file := filepath.Join(source, "scan-fixture", name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dest, "scan-fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	code := func(v map[string]json.RawMessage) int { var n int; _ = json.Unmarshal(v["code"], &n); return n }
	type response struct {
		Job    *nasScanJob `json:"job"`
		Result *struct {
			ID          string           `json:"scan_id"`
			Items       []nasCompareItem `json:"items"`
			CompletedAt time.Time        `json:"completed_at"`
		} `json:"result"`
	}
	decode := func(v map[string]json.RawMessage) response {
		t.Helper()
		if code(v) != 200 {
			t.Fatal(v)
		}
		var r response
		if err := json.Unmarshal(v["data"], &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	request := map[string]any{"src_dir": "/cloud/scan-fixture", "dst_dir": "/nas/scan-fixture", "names": []string{"chosen", "single.bin", "chosen"}}
	if code(invoke(BitQiuNASScanStart, map[string]any{"src_dir": "/cloud", "dst_dir": "/nas"}, true)) == 200 {
		t.Fatal("accepted empty scope")
	}
	d, _ := op.GetStorageByMountPath("/cloud")
	fixture := d.(*nasFixtureDriver)
	old := fixture.streamHook
	defer func() { fixture.streamHook = old }()
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	fixture.streamHook = func(ctx context.Context, objs []model.Obj, args model.ListArgs) ([]model.Obj, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if args.OnPage != nil {
			if err := args.OnPage(objs); err != nil {
				return nil, err
			}
		}
		return objs, nil
	}
	first := decode(invoke(BitQiuNASScanStart, request, true))
	if first.Job == nil || first.Job.ID == "" {
		t.Fatal(first)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background worker did not start")
	}
	repeated := decode(invoke(BitQiuNASScanStart, request, true))
	if repeated.Job.ID != first.Job.ID {
		t.Fatal("duplicate scan started")
	}
	status := func(id string) response {
		return decode(invoke(BitQiuNASScanStatus, map[string]any{"job_id": id}, true))
	}
	progress := status(first.Job.ID)
	if progress.Job.Percent != -1 || progress.Job.Discovered < 1 {
		t.Fatal("invented enumeration percentage", progress.Job)
	}
	close(gate)
	wait := func(id string) response {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			r := status(id)
			if r.Job != nil && !scanRunning(r.Job.State) && r.Job.FinishedAt != nil {
				return r
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("scan did not finish")
		return response{}
	}
	complete := wait(first.Job.ID)
	if complete.Job.State != "completed" || complete.Job.Percent != 100 || complete.Job.Total != 3 || complete.Result == nil || complete.Result.CompletedAt.IsZero() {
		t.Fatalf("bad final status %+v", complete)
	}
	if len(complete.Result.Items) != 3 {
		t.Fatal("unselected range included", complete.Result.Items)
	}
	for _, item := range complete.Result.Items {
		if strings.Contains(item.Relative, "unselected") {
			t.Fatal("scanned unselected object")
		}
	}
	// A disconnected request does not own/cancel the accepted background job.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(map[string]any{"job_id": first.Job.ID})
	recorder := httptestCompareContext(t, raw, &model.User{ID: 2, BasePath: "/", Role: model.Roles{model.ADMIN}}, ctx, BitQiuNASScanStatus)
	if strings.Contains(recorder.Body.String(), first.Job.ID) {
		t.Fatal("another admin can read private scan")
	}
	// Restart recovery keeps complete results and marks a persisted active run interrupted.
	nasScanStore.Lock()
	for _, j := range nasScanStore.jobs {
		if j.ID == first.Job.ID {
			j.State = "checking"
			j.FinishedAt = nil
		}
	}
	if err := saveNASScansLocked(); err != nil {
		t.Fatal(err)
	}
	nasScanStore.loaded = false
	nasScanStore.jobs = map[string]*nasScanJob{}
	nasScanStore.Unlock()
	restored := status(first.Job.ID)
	if restored.Job.State != "interrupted" || restored.Result == nil || restored.Result.ID != first.Job.ID {
		t.Fatal("restart discarded complete cache", restored)
	}
	if s := restoreNASScan(&model.User{ID: 1}, first.Job.ID); s == nil || len(s.items) != 3 || s.items[0].src == "" {
		t.Fatal("durable result cannot generate plans")
	}
	// Cancellation of the next run retains the previous complete cache for exactly this scope.
	gate = make(chan struct{})
	entered = make(chan struct{}, 1)
	second := decode(invoke(BitQiuNASScanStart, request, true))
	if second.Job.ID == first.Job.ID {
		t.Fatal("new scan not created")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second scan not running")
	}
	decode(invoke(BitQiuNASScanCancel, map[string]any{"job_id": second.Job.ID}, true))
	canceled := wait(second.Job.ID)
	if canceled.Job.State != "canceled" || canceled.Result == nil || canceled.Result.ID != first.Job.ID {
		t.Fatal("cancel discarded cache", canceled)
	}
	close(gate)
	different := decode(invoke(BitQiuNASScanStatus, map[string]any{"src_dir": "/cloud/scan-fixture", "dst_dir": "/nas/scan-fixture", "names": []string{"single.bin"}}, true))
	if different.Job != nil || different.Result != nil {
		t.Fatal("cache crossed scope")
	}
	for _, handler := range []gin.HandlerFunc{BitQiuNASScanStart, BitQiuNASScanStatus, BitQiuNASScanCancel} {
		protected := func(c *gin.Context) {
			middlewares.AuthAdmin(c)
			if !c.IsAborted() {
				handler(c)
			}
		}
		if code(invoke(protected, request, false)) != 403 {
			t.Fatal("scan endpoint lacks admin restriction")
		}
	}
}
