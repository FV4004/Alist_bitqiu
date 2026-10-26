package handles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/xhofe/tache"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"github.com/gin-gonic/gin"
)

func TestInventoryEvidenceMatching(t *testing.T) {
	a := nasInventoryObject{Path: "a/movie.mkv", Size: 10, SizeKnown: true, Hashes: map[string]string{"md5": "abcd"}, HashVerified: true}
	cases := []struct {
		name   string
		target map[string]nasInventoryObject
		cross  bool
		kind   string
		count  int
	}{
		{"same path different size", map[string]nasInventoryObject{a.Path: {Path: a.Path, Size: 11, SizeKnown: true}}, false, "size_difference", 1},
		{"unknown is not zero", map[string]nasInventoryObject{a.Path: {Path: a.Path, Size: 0, SizeKnown: false}}, false, "indeterminate", 1},
		{"same path unverified", map[string]nasInventoryObject{a.Path: {Path: a.Path, Size: 10, SizeKnown: true}}, false, "content_unverified", 1},
		{"legacy flag cannot restrict names", map[string]nasInventoryObject{"b/movie.mkv": {Path: "b/movie.mkv", Size: 10, SizeKnown: true}}, false, "suspected_move", 1},
		{"move scoped on", map[string]nasInventoryObject{"b/movie.mkv": {Path: "b/movie.mkv", Size: 10, SizeKnown: true}}, true, "suspected_move", 1},
		{"rename", map[string]nasInventoryObject{"a/movie_.mkv": {Path: "a/movie_.mkv", Size: 10, SizeKnown: true}}, false, "suspected_rename", 1},
		{"ambiguity", map[string]nasInventoryObject{"b/movie.mkv": {Path: "b/movie.mkv"}, "c/movie.mkv": {Path: "c/movie.mkv"}}, true, "suspected_move", 2},
		{"hash rename", map[string]nasInventoryObject{"a/different.bin": {Path: "a/different.bin", Size: 10, SizeKnown: true, HashVerified: true, Hashes: a.Hashes}}, true, "suspected_rename", 1},
		{"reported hash not trusted", map[string]nasInventoryObject{"a/different.bin": {Path: "a/different.bin", Size: 10, SizeKnown: true, HashVerified: false, Hashes: a.Hashes}}, true, "suspected_missing", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, _, candidates, _ := inventoryMatch(a, c.target, c.cross)
			if kind != c.kind || len(candidates) != c.count {
				t.Fatalf("%s %d", kind, len(candidates))
			}
		})
	}
}
func TestInventoryReviewFingerprintAndInvalidation(t *testing.T) {
	a := nasInventoryObject{Path: "test", Size: 2, SizeKnown: true, ScannedAt: time.Now()}
	d := &nasInventoryDetail{Source: &a, Reasons: []string{"missing"}}
	d.Fingerprint = inventoryFingerprint(d)
	original := d.Fingerprint
	a.ScannedAt = time.Now().Add(time.Hour)
	if inventoryFingerprint(d) != original {
		t.Fatal("scan time invalidates unchanged evidence")
	}
	d.Reviews = []nasReviewEvent{{Decision: "missing", Fingerprint: original}}
	old := &nasScanResult{Items: []nasScanStoredItem{{Item: nasCompareItem{ID: "x", Detail: d}}}}
	a.Size = 3
	next := &nasInventoryDetail{Source: &a}
	next.Fingerprint = inventoryFingerprint(next)
	result := &nasScanResult{Items: []nasScanStoredItem{{Item: nasCompareItem{ID: "x", Detail: next}}}}
	carryInventoryReviews(result, old)
	if !next.Stale || inventoryDecision(next) != "pending" || len(next.Reviews) != 1 {
		t.Fatal("changed evidence reused old conclusion")
	}
}
func TestInventoryRootedHashAndLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	v, err := inventoryNAS(context.Background(), root, ".", "file", true)
	if err != nil || len(v.Hashes["sha256"]) != 64 || len(v.Hashes["md5"]) != 32 {
		t.Fatal(v, err)
	}
	if _, err = inventoryNAS(context.Background(), root, ".", "link", true); err == nil {
		t.Fatal("read link")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = inventoryNAS(ctx, root, ".", "file", true); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestInventoryCSVFormula(t *testing.T) {
	for _, s := range []string{"=CMD()", " +1", "\t@SUM()", "-3"} {
		if inventoryCSVCell(s)[0] != '\'' {
			t.Fatal("unsafe cell", s)
		}
	}
}

func testNASInventoryWorkflow(t *testing.T, source, dest string, invoke func(gin.HandlerFunc, any, bool) map[string]json.RawMessage) {
	write := func(base, name, contents string) {
		t.Helper()
		f := filepath.Join(base, "inventory-fixture", name)
		if err := os.MkdirAll(filepath.Dir(f), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "chosen/missing.txt", "missing")
	write(source, "chosen/same.txt", "source larger")
	write(dest, "chosen/same.txt", "nas")
	write(source, "chosen/old_name.txt", "rename")
	write(dest, "chosen/old-name.txt", "rename")
	write(dest, "chosen/only.txt", "nas only")
	write(source, "chosen/moved.txt", "move")
	write(dest, "elsewhere/moved.txt", "move")
	write(source, "chosen/ambiguous.txt", "multiple")
	write(dest, "candidate-a/ambiguous.txt", "multiple")
	write(dest, "candidate-b/ambiguous.txt", "multiple")
	// Batch validation lists a paginated parent once per request, and refreshes
	// it again for the separate final-confirmation request.
	batchDriver, _ := op.GetStorageByMountPath("/cloud")
	batchFixture := batchDriver.(*nasFixtureDriver)
	batchHook := batchFixture.streamHook
	listCalls := 0
	batchFixture.streamHook = func(ctx context.Context, objs []model.Obj, args model.ListArgs) ([]model.Obj, error) {
		listCalls++
		return objs, nil
	}
	batchCtx := inventoryValidationContext(context.Background())
	for _, name := range []string{"missing.txt", "same.txt"} {
		if _, err := inventoryFreshObject(batchCtx, batchDriver, "/inventory-fixture/chosen/"+name); err != nil {
			t.Fatal(err)
		}
	}
	if listCalls != 1 {
		t.Fatal("preflight repeated full pagination per file", listCalls)
	}
	if _, err := inventoryFreshObject(inventoryValidationContext(context.Background()), batchDriver, "/inventory-fixture/chosen/missing.txt"); err != nil {
		t.Fatal(err)
	}
	if listCalls != 2 {
		t.Fatal("confirmation reused earlier preflight data")
	}
	batchFixture.streamHook = batchHook
	code := func(v map[string]json.RawMessage) int { var n int; _ = json.Unmarshal(v["code"], &n); return n }
	type view struct {
		Job    *nasScanJob `json:"job"`
		Result *struct {
			ID    string           `json:"scan_id"`
			Items []nasCompareItem `json:"items"`
		} `json:"result"`
	}
	decode := func(v map[string]json.RawMessage) view {
		t.Helper()
		if code(v) != 200 {
			t.Fatal(v)
		}
		var r view
		if err := json.Unmarshal(v["data"], &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	req := map[string]any{"src_dir": "/cloud/inventory-fixture", "dst_dir": "/nas/inventory-fixture", "names": []string{"chosen"}, "options": nasInventoryOptions{Enabled: true, NASOnly: true, FullHash: true, CrossDirectory: false}}
	scan := func() view {
		t.Helper()
		r := decode(invoke(BitQiuNASScanStart, req, true))
		deadline := time.Now().Add(10 * time.Second)
		for scanRunning(r.Job.State) && time.Now().Before(deadline) {
			time.Sleep(15 * time.Millisecond)
			r = decode(invoke(BitQiuNASScanStatus, map[string]any{"job_id": r.Job.ID}, true))
		}
		if r.Job.State != "completed" || r.Result == nil {
			t.Fatalf("scan failed: %+v", r.Job)
		}
		return r
	}
	r := scan()
	if r.Job.Options.Matching != inventoryMatchingVersion || !r.Job.Options.CrossDirectory {
		t.Fatal("scan did not normalize name matching options")
	}
	for _, v := range r.Result.Items {
		if v.Relative == "elsewhere/moved.txt" && v.Category == "nas_only" {
			t.Fatal("matched NAS candidate also counted as NAS-only")
		}
	}
	byPath := map[string]nasCompareItem{}
	for _, v := range r.Result.Items {
		byPath[v.Relative] = v
	}
	for rel, kind := range map[string]string{"chosen/missing.txt": "suspected_missing", "chosen/same.txt": "size_difference", "chosen/old_name.txt": "suspected_rename", "chosen/only.txt": "nas_only", "chosen/moved.txt": "suspected_move", "chosen/ambiguous.txt": "suspected_move"} {
		if byPath[rel].Category != kind {
			t.Fatalf("%s: %+v", rel, byPath[rel])
		}
	}
	if len(byPath["chosen/ambiguous.txt"].Detail.Candidates) != 2 {
		t.Fatal("lost ambiguity across NAS directories")
	}
	if len(byPath["chosen/moved.txt"].Detail.Candidates) != 1 {
		t.Fatal("did not inventory the chosen NAS root")
	}
	v := byPath["chosen/missing.txt"]
	before := decode(invoke(BitQiuNASScanStatus, map[string]any{"job_id": r.Job.ID}, true))
	_ = before
	if code(invoke(BitQiuNASInventoryReview, map[string]any{"scan_id": r.Result.ID, "ids": []string{v.ID}, "decision": "missing", "fingerprints": map[string]string{v.ID: "stale"}}, true)) == 200 {
		t.Fatal("accepted stale review")
	}
	reviewReq := map[string]any{"scan_id": r.Result.ID, "ids": []string{v.ID}, "decision": "missing", "fingerprints": map[string]string{v.ID: v.Detail.Fingerprint}, "note": "checked"}
	if code(invoke(BitQiuNASInventoryReview, reviewReq, false)) == 200 {
		t.Fatal("guest review")
	}
	decode(invoke(BitQiuNASInventoryReview, reviewReq, true))
	prepared := invoke(BitQiuNASComparePrepare, map[string]any{"scan_id": r.Result.ID, "ids": []string{v.ID}}, true)
	if code(prepared) != 200 {
		t.Fatal(prepared)
	}
	var plan struct {
		ID    string           `json:"plan_id"`
		Tasks []nasCompareTask `json:"tasks"`
	}
	_ = json.Unmarshal(prepared["data"], &plan)
	if len(plan.Tasks) != 1 {
		t.Fatal("missing reviewed task", prepared)
	}
	// Change review after preparation: final confirmation must reject stale consent.
	reviewReq["decision"] = "pending"
	decode(invoke(BitQiuNASInventoryReview, reviewReq, true))
	committed := invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.ID, "confirmed": true}, true)
	if code(committed) != 200 {
		t.Fatal(committed)
	}
	var outcome struct {
		Queued int `json:"queued"`
	}
	_ = json.Unmarshal(committed["data"], &outcome)
	if outcome.Queued != 0 {
		t.Fatal("queued revoked conclusion")
	}
	if code(invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.ID, "confirmed": true}, true)) == 200 {
		t.Fatal("duplicate commit")
	}
	reviewReq["decision"] = "missing"
	decode(invoke(BitQiuNASInventoryReview, reviewReq, true))
	other := func(c *gin.Context) {
		u := *c.MustGet("user").(*model.User)
		u.ID = 2
		c.Set("user", &u)
		BitQiuNASInventoryReview(c)
	}
	if code(invoke(other, reviewReq, true)) == 200 {
		t.Fatal("cross-owner review")
	}
	// Recheck NAS existence at final confirmation, preserving external contents.
	prepared = invoke(BitQiuNASComparePrepare, map[string]any{"scan_id": r.Result.ID, "ids": []string{v.ID}}, true)
	_ = json.Unmarshal(prepared["data"], &plan)
	write(dest, "chosen/missing.txt", "external NAS data")
	committed = invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.ID, "confirmed": true}, true)
	_ = json.Unmarshal(committed["data"], &outcome)
	if outcome.Queued != 0 {
		t.Fatal("queued existing target")
	}
	got, _ := os.ReadFile(filepath.Join(dest, "inventory-fixture/chosen/missing.txt"))
	if string(got) != "external NAS data" {
		t.Fatal("overwrote target")
	}
	if err := os.Remove(filepath.Join(dest, "inventory-fixture/chosen/missing.txt")); err != nil {
		t.Fatal(err)
	}
	prepared = invoke(BitQiuNASComparePrepare, map[string]any{"scan_id": r.Result.ID, "ids": []string{v.ID}}, true)
	_ = json.Unmarshal(prepared["data"], &plan)
	committed = invoke(BitQiuNASCompareCommit, map[string]any{"plan_id": plan.ID, "confirmed": true}, true)
	_ = json.Unmarshal(committed["data"], &outcome)
	if outcome.Queued != 1 {
		t.Fatal("confirmed explicit selection not queued", committed)
	}
	duplicate := invoke(BitQiuNASComparePrepare, map[string]any{"scan_id": r.Result.ID, "ids": []string{v.ID}}, true)
	var duplicatePlan struct {
		Tasks []nasCompareTask `json:"tasks"`
	}
	_ = json.Unmarshal(duplicate["data"], &duplicatePlan)
	if len(duplicatePlan.Tasks) != 0 {
		t.Fatal("duplicate target task")
	}
	for _, task := range fs.CopyTaskManager.GetAll() {
		view, ok := task.NASView()
		if ok && view.Source == "/cloud/inventory-fixture/chosen/missing.txt" {
			worker := tache.Worker[*fs.CopyTask]{}
			worker.Execute(task)
			if task.GetState() != tache.StateSucceeded {
				t.Fatal(task.GetErr())
			}
		}
	}
	got, _ = os.ReadFile(filepath.Join(dest, "inventory-fixture/chosen/missing.txt"))
	if string(got) != "missing" {
		t.Fatal("queued transfer failed")
	}
	for _, format := range []string{"csv", "json"} {
		body, _ := json.Marshal(map[string]any{"scan_id": r.Result.ID, "format": format, "groups": []string{"confirmed_missing", "issues"}})
		response := httptestCompareContext(t, body, &model.User{ID: 1, BasePath: "/", Role: model.Roles{model.ADMIN}}, context.Background(), BitQiuNASInventoryExport)
		if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte("checked")) {
			t.Fatal("export lost review evidence", response.Body.String())
		}
	}
	write(source, "chosen/missing.txt", "different payload")
	r = scan()
	for _, item := range r.Result.Items {
		if item.Relative == v.Relative && (!item.Detail.Stale || inventoryDecision(item.Detail) != "pending") {
			t.Fatal("review not invalidated")
		}
	}
	// Cached inventory reviews survive loading from the existing durable journal.
	nasScanStore.Lock()
	nasScanStore.loaded = false
	nasScanStore.jobs = map[string]*nasScanJob{}
	nasScanStore.Unlock()
	restored := decode(invoke(BitQiuNASScanStatus, map[string]any{"latest": true}, true))
	if restored.Result == nil {
		t.Fatal("lost persisted result")
	}
	// A single selected source file still sees renamed siblings inside the
	// explicit NAS root. Cross-directory matching uses that root, never its parent.
	write(source, "old-root.txt", "root rename")
	write(dest, "old_root.txt", "root rename")
	savedReq := req
	req = map[string]any{"src_dir": "/cloud/inventory-fixture", "dst_dir": "/nas/inventory-fixture", "names": []string{"old-root.txt"}, "options": nasInventoryOptions{Enabled: true}}
	single := scan()
	if len(single.Result.Items) != 1 || single.Result.Items[0].Category != "suspected_rename" || len(single.Result.Items[0].Detail.Candidates) != 1 {
		t.Fatal("single-file scan lost renamed sibling", single.Result.Items)
	}
	req = savedReq
	// A new incomplete scan preserves the complete result and exposes issues.
	driver, _ := op.GetStorageByMountPath("/cloud")
	fixture := driver.(*nasFixtureDriver)
	oldHook := fixture.streamHook
	defer func() { fixture.streamHook = oldHook }()
	fixture.streamHook = func(ctx context.Context, objs []model.Obj, args model.ListArgs) ([]model.Obj, error) {
		for _, o := range objs {
			if o.GetName() == "missing.txt" {
				return nil, errors.New("incomplete paginated listing / permission denied")
			}
		}
		return objs, nil
	}
	started := decode(invoke(BitQiuNASScanStart, req, true))
	deadline := time.Now().Add(10 * time.Second)
	var raw map[string]json.RawMessage
	for time.Now().Before(deadline) {
		raw = invoke(BitQiuNASScanStatus, map[string]any{"job_id": started.Job.ID}, true)
		started = decode(raw)
		if !scanRunning(started.Job.State) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if started.Job.State != "partial" || started.Result == nil || started.Result.ID != r.Result.ID {
		t.Fatal("incomplete scan replaced complete cache", started.Job)
	}
	if !bytes.Contains(raw["data"], []byte(`"attempt"`)) || !bytes.Contains(raw["data"], []byte("无法判断")) {
		t.Fatal("partial issues not exposed")
	}
	fixture.streamHook = oldHook
	// A mutation after enumeration must produce an incomplete attempt.
	mutated := false
	fixture.streamHook = func(ctx context.Context, objs []model.Obj, args model.ListArgs) ([]model.Obj, error) {
		for _, o := range objs {
			if o.GetName() == "missing.txt" && !mutated {
				mutated = true
				write(source, "chosen/missing.txt", "changed during enumeration")
			}
		}
		return objs, nil
	}
	started = decode(invoke(BitQiuNASScanStart, req, true))
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		started = decode(invoke(BitQiuNASScanStatus, map[string]any{"job_id": started.Job.ID}, true))
		if !scanRunning(started.Job.State) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if started.Job.State != "partial" {
		t.Fatal("concurrent source change not detected", started.Job)
	}
	fixture.streamHook = oldHook
}
