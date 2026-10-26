package handles

// Inventory extends the existing background scan and rooted NAS reader. It never
// requests a download link, persists driver additions, or writes into the NAS.
import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

type nasInventoryOptions struct {
	Enabled        bool   `json:"enabled"`
	Matching       string `json:"matching,omitempty"`
	NASOnly        bool   `json:"nas_only"`
	CrossDirectory bool   `json:"cross_directory"`
	FullHash       bool   `json:"full_hash"`
}
type nasInventoryObject struct {
	Side      string            `json:"side,omitempty"`
	Path      string            `json:"path"`
	ObjectID  string            `json:"object_id"`
	IsDir     bool              `json:"is_dir"`
	Size      int64             `json:"size"`
	SizeKnown bool              `json:"size_known"`
	Modified  time.Time         `json:"modified"`
	ScannedAt time.Time         `json:"scanned_at"`
	Hashes    map[string]string `json:"hashes,omitempty"`
	// Reported Bitqiu fileMd5 is retained as evidence but is NOT assumed to be a
	// full-content hash until its semantics have been verified on real hardware.
	HashVerified bool   `json:"hash_verified"`
	Version      string `json:"version,omitempty"`
}
type nasEvidenceCandidate struct {
	Object  nasInventoryObject `json:"object"`
	Reasons []string           `json:"reasons"`
	Rank    int                `json:"rank"`
}
type nasReviewEvent struct {
	Decision     string          `json:"decision"`
	Candidate    string          `json:"candidate,omitempty"`
	Note         string          `json:"note"`
	Reviewer     uint            `json:"reviewer"`
	ReviewerName string          `json:"reviewer_name"`
	At           time.Time       `json:"at"`
	Fingerprint  string          `json:"fingerprint"`
	Evidence     json.RawMessage `json:"evidence"`
}
type nasInventoryDetail struct {
	Source      *nasInventoryObject    `json:"source,omitempty"`
	NAS         *nasInventoryObject    `json:"nas,omitempty"`
	Candidates  []nasEvidenceCandidate `json:"candidates"`
	Reasons     []string               `json:"reasons"`
	Fingerprint string                 `json:"fingerprint"`
	Reviews     []nasReviewEvent       `json:"reviews"`
	Stale       bool                   `json:"stale"`
}

func inventoryDigest(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func inventorySource(obj model.Obj, rel string, verified bool) nasInventoryObject {
	v := nasInventoryObject{Path: rel, ObjectID: obj.GetID(), IsDir: obj.IsDir(), Size: -1, Modified: obj.ModTime(), ScannedAt: time.Now(), Hashes: map[string]string{}, HashVerified: verified}
	if !obj.IsDir() && model.ObjectSizeKnown(obj) {
		v.Size = obj.GetSize()
		v.SizeKnown = true
	}
	for alg, value := range obj.GetHash().All() {
		b, err := hex.DecodeString(value)
		if err == nil && len(value) == alg.Width && len(b) > 0 {
			v.Hashes[alg.Name] = strings.ToLower(value)
		}
	}
	return v
}
func inventoryStable(v *nasInventoryObject) any {
	if v == nil {
		return nil
	}
	c := *v
	c.ScannedAt = time.Time{}
	return c
}
func inventoryFingerprint(v *nasInventoryDetail) string {
	candidates := make([]any, 0, len(v.Candidates))
	for _, c := range v.Candidates {
		candidates = append(candidates, []any{inventoryStable(&c.Object), c.Reasons})
	}
	return inventoryDigest([]any{inventoryStable(v.Source), inventoryStable(v.NAS), candidates, v.Reasons})
}
func inventoryEvidence(v *nasInventoryDetail) json.RawMessage {
	raw, _ := json.Marshal(gin.H{"source": v.Source, "nas": v.NAS, "candidates": v.Candidates, "reasons": v.Reasons})
	return raw
}
func inventoryDecision(v *nasInventoryDetail) string {
	if v == nil || v.Stale || len(v.Reviews) == 0 {
		return "pending"
	}
	e := v.Reviews[len(v.Reviews)-1]
	if e.Fingerprint != v.Fingerprint {
		return "pending"
	}
	return e.Decision
}
func inventorySame(a, b nasInventoryObject) bool {
	return a.ObjectID == b.ObjectID && a.IsDir == b.IsDir && a.SizeKnown == b.SizeKnown && (!a.SizeKnown || a.Size == b.Size) && a.Modified.Equal(b.Modified) && inventoryDigest(a.Hashes) == inventoryDigest(b.Hashes)
}
func inventoryHashMatch(a, b nasInventoryObject) bool {
	if !a.HashVerified || !b.HashVerified || a.IsDir || b.IsDir || inventoryHashConflict(a, b) || a.SizeKnown && b.SizeKnown && a.Size != b.Size {
		return false
	}
	for alg, value := range a.Hashes {
		if value != "" && b.Hashes[alg] == value {
			return true
		}
	}
	return false
}

// inventoryMatch searches the entire explicitly inventoried NAS scope. The legacy
// cross flag is accepted for API/cache compatibility but never restricts names to
// an assumed relative-path mapping.
func inventoryMatch(a nasInventoryObject, target map[string]nasInventoryObject, _ bool) (string, *nasInventoryObject, []nasEvidenceCandidate, []string) {
	return inventoryMatchPool(a, target)
}

func inventoryMatchPool(a nasInventoryObject, target map[string]nasInventoryObject) (string, *nasInventoryObject, []nasEvidenceCandidate, []string) {
	candidates := []nasEvidenceCandidate{}
	for _, b := range target {
		if a.IsDir != b.IsDir || b.Path == "." {
			continue
		}
		reasons := []string{}
		score := 0
		if inventoryHashMatch(a, b) {
			reasons = append(reasons, "同算法完整哈希一致")
			score += 1000
		}
		if path.Base(a.Path) == path.Base(b.Path) {
			reasons = append(reasons, "名称完全一致，内容尚需核对")
			score += 100
		} else if inventoryNormalizedNameMatch(a, b) {
			reasons = append(reasons, "名称归一化一致，内容尚需核对")
			score += 80
		} else if compareSimilar(path.Base(a.Path), path.Base(b.Path)) {
			reasons = append(reasons, "名称相似，不能证明内容相同")
			score += 40
		}
		if len(reasons) == 0 {
			continue
		}
		if !a.IsDir {
			if a.SizeKnown && b.SizeKnown {
				if a.Size == b.Size {
					reasons = append(reasons, "已知大小一致")
					score += 200
				} else {
					reasons = append(reasons, "已知大小不同，候选冲突")
					score -= 300
				}
			} else {
				reasons = append(reasons, "大小未知，不能视为零")
			}
			if inventoryHashConflict(a, b) {
				reasons = append(reasons, "同算法完整哈希不同，候选冲突")
				score -= 2000
			}
		}
		if folder := inventorySharedFolder(a.Path, b.Path); folder != "" {
			reasons = append(reasons, "所属文件夹名称一致："+folder+"（仅辅助证据）")
			score += 20
		}
		candidates = append(candidates, nasEvidenceCandidate{Object: b, Reasons: reasons, Rank: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Rank != candidates[j].Rank {
			return candidates[i].Rank > candidates[j].Rank
		}
		return candidates[i].Object.Path < candidates[j].Object.Path
	})
	// Record an existing destination as a write conflict, never as a shortcut
	// that hides matching names in other directories.
	var direct *nasInventoryObject
	if b, ok := target[a.Path]; ok {
		direct = &b
		if a.IsDir != b.IsDir {
			return "indeterminate", direct, candidates, []string{"下载目标同路径类型冲突；名称候选仍保留"}
		}
		if a.SizeKnown && b.SizeKnown && a.Size != b.Size {
			return "size_difference", direct, candidates, []string{"下载目标同路径大小不同；其他目录名称候选仍保留"}
		}
		if inventoryHashConflict(a, b) {
			return "content_difference", direct, candidates, []string{"同算法完整哈希不同；其他候选仍保留"}
		}
	}
	if len(candidates) == 1 && candidates[0].Object.Path == a.Path {
		b := candidates[0].Object
		if a.IsDir {
			return "directory_present", direct, candidates, []string{"目录名称一致；目录内容需逐项核对"}
		}
		if inventoryHashMatch(a, b) {
			return "content_verified", direct, candidates, []string{"同算法完整哈希一致"}
		}
		if !a.SizeKnown || !b.SizeKnown {
			return "indeterminate", direct, candidates, []string{"大小未知，不能确认内容一致"}
		}
		return "content_unverified", direct, candidates, []string{"名称与已知大小一致，内容未校验"}
	}
	if len(candidates) > 0 {
		kind := "suspected_rename"
		for _, c := range candidates {
			if path.Dir(c.Object.Path) != path.Dir(a.Path) {
				kind = "suspected_move"
				break
			}
		}
		return kind, direct, candidates, []string{"按名称在所选 NAS 全范围寻找候选，不要求路径对应；排序不等于确认，多个候选保留歧义"}
	}
	return "suspected_missing", direct, candidates, []string{"所选 NAS 范围内未发现名称或可信哈希候选；不代表已确认缺失"}
}

type inventoryContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r inventoryContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func inventoryNAS(ctx context.Context, root *os.Root, base, rel string, full bool) (nasInventoryObject, error) {
	v := nasInventoryObject{Path: rel, Size: -1, ScannedAt: time.Now(), HashVerified: true}
	name := path.Join(base, rel)
	if err := fs.NASCheckParents(root, name); err != nil {
		return v, err
	}
	version, info, err := fs.NASPathVersion(root, name)
	if err != nil {
		return v, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return v, errors.New("链接或特殊对象，未读取")
	}
	v.ObjectID = version
	v.Version = version
	v.IsDir = info.IsDir()
	v.Modified = info.ModTime()
	if !v.IsDir {
		v.Size = info.Size()
		v.SizeKnown = true
	}
	if full && !v.IsDir {
		f, e := root.Open(name)
		if e != nil {
			return v, e
		}
		defer f.Close()
		opened, e := f.Stat()
		if e != nil || !os.SameFile(info, opened) {
			return v, errors.New("NAS 对象在打开时变化")
		}
		m, h := md5.New(), sha256.New()
		n, e := io.Copy(io.MultiWriter(m, h), inventoryContextReader{ctx, f})
		if e != nil {
			return v, e
		}
		after, _, e := fs.NASPathVersion(root, name)
		if e != nil || after != version || n != v.Size {
			return v, errors.New("NAS 文件在哈希期间变化")
		}
		v.Hashes = map[string]string{"md5": hex.EncodeToString(m.Sum(nil)), "sha256": hex.EncodeToString(h.Sum(nil))}
	}
	return v, nil
}
func scanNASInventory(ctx context.Context, j *nasScanJob, s *nasComparison, actual string, root *os.Root, emit func(string, int) error) error {
	source := map[string]nasInventoryObject{}
	target := map[string]nasInventoryObject{}
	visited := 0
	issue := func(rel, msg string) { s.issues = append(s.issues, nasCompareIssue{rel, msg}) }
	// The source inventory is the same complete paginated listing used by the
	// background scan. No extra traversal outside the selected names is allowed.
	for dir, children := range s.inventory {
		for _, obj := range children {
			rel := strings.TrimPrefix(strings.TrimPrefix(path.Join(dir, obj.GetName()), actual), "/")
			if !common.CanReadPathByRole(s.user, path.Join(s.sourceVirtual, rel)) {
				issue(rel, "来源无读取权限")
				continue
			}
			source[rel] = inventorySource(obj, rel, s.source.Config().Name != "BitQiu")
		}
	}
	for dir := range s.inventoryErrors {
		rel := strings.TrimPrefix(strings.TrimPrefix(dir, actual), "/")
		issue(rel, "来源分页、权限或目录读取不完整，无法判断")
	}
	var walk func(string, int) error
	walk = func(rel string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if visited > compareLimit || depth > 128 {
			return errors.New("NAS 盘点超过范围上限，请缩小范围")
		}
		if err := emit(rel, visited); err != nil {
			return err
		}
		if !common.CanReadPathByRole(s.user, path.Join(s.destination, rel)) {
			issue(rel, "NAS 路径无读取权限")
			return nil
		}
		v, err := inventoryNAS(ctx, root, s.targetBase, rel, j.Options.FullHash)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			issue(rel, "NAS 无法读取或扫描期间变化")
			return nil
		}
		target[rel] = v
		if !v.IsDir {
			return nil
		}
		f, err := root.Open(path.Join(s.targetBase, rel))
		if err != nil {
			issue(rel, "NAS 目录无法读取")
			return nil
		}
		children, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			issue(rel, "NAS 目录未完整读取")
			return nil
		}
		for _, entry := range children {
			if err := walk(path.Join(rel, entry.Name()), depth+1); err != nil {
				return err
			}
		}
		after, _, err := fs.NASPathVersion(root, path.Join(s.targetBase, rel))
		if err != nil || after != v.Version {
			issue(rel, "NAS 目录在扫描期间变化")
		}
		return nil
	}
	// The chosen NAS directory is its own explicit scope. Enumerate its full
	// subtree, including siblings of selected source files, so renamed or moved
	// counterparts cannot disappear from the candidate pool.
	if err := walk(".", 0); err != nil {
		return err
	}
	// Refresh complete parent listings, rather than op.Get's directory cache.
	// Also compare directory membership so newly added files are not missed.
	for dir, before := range s.inventory {
		if err := ctx.Err(); err != nil {
			return err
		}
		after, err := op.List(ctx, s.source, dir, model.ListArgs{Refresh: true, NoUpdateIndex: true, OnPage: func(_ []model.Obj) error { return ctx.Err() }})
		relDir := strings.TrimPrefix(strings.TrimPrefix(dir, actual), "/")
		if err != nil {
			issue(relDir, "来源复查分页未完整读取")
			continue
		}
		current := map[string]model.Obj{}
		for _, obj := range after {
			current[obj.GetName()] = obj
		}
		if dir != actual && len(after) != len(before) {
			issue(relDir, "来源目录成员在扫描期间变化")
		}
		for _, obj := range before {
			rel := path.Join(relDir, obj.GetName())
			a, ok := source[rel]
			if !ok {
				continue
			}
			next, ok := current[obj.GetName()]
			if !ok || !inventorySame(a, inventorySource(next, rel, a.HashVerified)) {
				issue(rel, "来源在扫描期间变化或无法复查")
			}
		}
	}
	// Recheck NAS versions after the entire scan, including files hashed early.
	for rel, v := range target {
		version, _, err := fs.NASPathVersion(root, path.Join(s.targetBase, rel))
		if err != nil || version != v.Version {
			issue(rel, "NAS 对象在扫描期间变化")
		}
	}
	for _, obj := range source {
		obj.Side = "bitqiu"
		s.inventoryObjects = append(s.inventoryObjects, obj)
	}
	for _, obj := range target {
		if obj.Path == "." {
			continue
		}
		obj.Side = "nas"
		s.inventoryObjects = append(s.inventoryObjects, obj)
	}
	sort.Slice(s.inventoryObjects, func(i, j int) bool {
		return s.inventoryObjects[i].Side+s.inventoryObjects[i].Path < s.inventoryObjects[j].Side+s.inventoryObjects[j].Path
	})
	// Index names and trusted hashes once; bound fuzzy comparisons and evidence.
	index := newInventoryNameIndex(target)
	work := 0
	candidateCount := 0
	usedTargets := map[string]bool{}
	keys := make([]string, 0, len(source))
	for rel := range source {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := source[rel]
		pool, err := index.pool(a, &work)
		if err != nil {
			return err
		}
		kind, b, candidates, reasons := inventoryMatchPool(a, pool)
		for _, c := range candidates {
			usedTargets[c.Object.Path] = true
		}
		if b != nil {
			usedTargets[b.Path] = true
		}
		candidateCount += len(candidates)
		if candidateCount > 200000 {
			return errors.New("候选证据超过 200000 条，请缩小范围；未截断候选")
		}
		blocked := len(s.issues) > 0 // Global name candidates require a complete scope.
		if blocked {
			kind = "indeterminate"
			reasons = append(reasons, "相关分支扫描不完整，无法判断")
		}
		detail := &nasInventoryDetail{Source: &a, NAS: b, Candidates: candidates, Reasons: reasons, Reviews: []nasReviewEvent{}}
		detail.Fingerprint = inventoryFingerprint(detail)
		names := []string{}
		for _, c := range candidates {
			names = append(names, c.Object.Path)
		}
		s.items = append(s.items, nasCompareItem{ID: inventoryDigest([]string{"source", rel}), Relative: rel, IsDir: a.IsDir, Category: kind, Size: a.Size, Candidates: names, Blocked: blocked || kind == "indeterminate", ObjectID: a.ObjectID, Detail: detail, src: path.Join(actual, rel)})
	}
	if j.Options.NASOnly {
		keys = keys[:0]
		for rel := range target {
			if rel == "." {
				continue
			}
			if !usedTargets[rel] {
				keys = append(keys, rel)
			}
		}
		sort.Strings(keys)
		for _, rel := range keys {
			v := target[rel]
			kind := "nas_only"
			reasons := []string{"在本次所选来源中无名称或可信哈希候选；不代表其他网盘范围不存在"}
			if len(s.issues) > 0 {
				kind = "indeterminate"
				reasons = []string{"来源存在未完成分支，无法判断是否 NAS 独有"}
			}
			d := &nasInventoryDetail{NAS: &v, Reasons: reasons, Candidates: []nasEvidenceCandidate{}, Reviews: []nasReviewEvent{}}
			d.Fingerprint = inventoryFingerprint(d)
			s.items = append(s.items, nasCompareItem{ID: inventoryDigest([]string{"nas", rel}), Relative: rel, IsDir: v.IsDir, Category: kind, Size: v.Size, Blocked: true, Detail: d})
		}
	}
	return nil
}

func carryInventoryReviews(result, old *nasScanResult) {
	if old == nil {
		return
	}
	byID := map[string]*nasInventoryDetail{}
	for _, v := range old.Items {
		if v.Item.Detail != nil {
			byID[v.Item.ID] = v.Item.Detail
		}
	}
	for i := range result.Items {
		d := result.Items[i].Item.Detail
		prev := byID[result.Items[i].Item.ID]
		if d == nil || prev == nil {
			continue
		}
		d.Reviews = append([]nasReviewEvent(nil), prev.Reviews...)
		if len(d.Reviews) > 0 {
			d.Stale = d.Reviews[len(d.Reviews)-1].Fingerprint != d.Fingerprint
		}
	}
}
func inventoryResultLocked(user *model.User, id string) (*nasScanJob, *nasScanResult, error) {
	if err := loadNASScansLocked(); err != nil {
		return nil, nil, err
	}
	for _, j := range nasScanStore.jobs {
		if j.Owner == user.ID && j.Result != nil && j.Result.ID == id {
			if !common.CanReadPathByRole(user, j.Source) || !common.CanReadPathByRole(user, j.Destination) {
				return nil, nil, errors.New("扫描路径权限已变化")
			}
			if !j.Options.Enabled {
				return nil, nil, errors.New("旧扫描不支持持久化复核，请重新盘点")
			}
			return j, j.Result, nil
		}
	}
	return nil, nil, errors.New("完整扫描不存在或已过期，请重新盘点")
}
func BitQiuNASInventoryReview(c *gin.Context) {
	var req struct {
		ScanID       string            `json:"scan_id"`
		IDs          []string          `json:"ids"`
		Fingerprints map[string]string `json:"fingerprints"`
		Decision     string            `json:"decision"`
		Candidate    string            `json:"candidate"`
		Note         string            `json:"note"`
		Confirmed    bool              `json:"confirmed"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 || len(req.IDs) > 1000 || len(req.Note) > 4000 || len(req.IDs) > 1 && !req.Confirmed {
		common.ErrorStrResp(c, "请明确勾选至多 1000 项并确认批量范围", 400)
		return
	}
	switch req.Decision {
	case "missing", "exists", "ignored", "pending", "undo":
	default:
		common.ErrorStrResp(c, "无效复核结论", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	if !user.IsAdmin() {
		common.ErrorStrResp(c, "仅管理员可访问盘点复核", 403)
		return
	}
	nasScanStore.Lock()
	defer nasScanStore.Unlock()
	j, r, err := inventoryResultLocked(user, req.ScanID)
	if err != nil {
		common.ErrorResp(c, err, 409)
		return
	}
	if err := inventoryRequireNameMatching(j); err != nil {
		common.ErrorResp(c, err, 409)
		return
	}
	index := map[string]int{}
	for i, v := range r.Items {
		index[v.Item.ID] = i
	}
	changed := map[int]nasCompareItem{}
	for _, id := range req.IDs {
		n, ok := index[id]
		if !ok {
			common.ErrorStrResp(c, "选择项不属于当前完整扫描", 400)
			return
		}
		v := r.Items[n].Item
		d := v.Detail
		if d == nil || req.Fingerprints[id] != d.Fingerprint || len(d.Reviews) >= 100 {
			common.ErrorStrResp(c, "证据已变化或复核记录达到上限，请刷新或重新盘点", 409)
			return
		}
		if req.Decision == "missing" && (v.Blocked || d.Source == nil || d.NAS != nil) {
			common.ErrorStrResp(c, "同路径已存在或无法判断的项目不能确认缺失", 409)
			return
		}
		if req.Decision == "exists" {
			found := false
			for _, candidate := range d.Candidates {
				if candidate.Object.Path == req.Candidate {
					found = true
				}
			}
			if !found {
				common.ErrorStrResp(c, "请选择当前候选进行关联", 400)
				return
			}
		}
		copy := *d
		copy.Reviews = append([]nasReviewEvent(nil), d.Reviews...)
		decision, candidate := req.Decision, req.Candidate
		if decision == "undo" {
			decision = "pending"
			candidate = ""
			if len(d.Reviews) > 1 {
				prev := d.Reviews[len(d.Reviews)-2]
				if prev.Fingerprint == d.Fingerprint {
					decision, candidate = prev.Decision, prev.Candidate
				}
			}
		}
		copy.Stale = false
		copy.Reviews = append(copy.Reviews, nasReviewEvent{Decision: decision, Candidate: candidate, Note: req.Note, Reviewer: user.ID, ReviewerName: user.Username, At: time.Now(), Fingerprint: d.Fingerprint, Evidence: inventoryEvidence(d)})
		v.Detail = &copy
		changed[n] = v
	}
	old := r.Items
	r.Items = append([]nasScanStoredItem(nil), old...)
	for n, v := range changed {
		r.Items[n].Item = v
	}
	if err := saveNASScansLocked(); err != nil {
		r.Items = old
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, nasScanViewLocked(nasScanStore.jobs[findInventoryKey(req.ScanID)], true))
}
func findInventoryKey(id string) string {
	for k, j := range nasScanStore.jobs {
		if j.Result != nil && j.Result.ID == id {
			return k
		}
	}
	return ""
}
func inventoryPreflight(ctx context.Context, user *model.User, scanID string, v nasCompareItem) error {
	nasScanStore.Lock()
	defer nasScanStore.Unlock()
	j, r, err := inventoryResultLocked(user, scanID)
	if err != nil {
		return err
	}
	if err := inventoryRequireNameMatching(j); err != nil {
		return err
	}
	if scanRunning(j.State) || j.State != "completed" {
		return errors.New("扫描正在进行或新扫描未完整完成，请重新盘点")
	}
	var current *nasInventoryDetail
	for _, item := range r.Items {
		if item.Item.ID == v.ID {
			current = item.Item.Detail
			break
		}
	}
	if current == nil || current.Fingerprint != v.Detail.Fingerprint || inventoryDecision(current) != "missing" {
		return errors.New("仅允许人工确认缺失且证据未变化的明确选择项")
	}
	d, _, _, err := nasSelectionSource(user, j.Source)
	if err != nil {
		return err
	}
	obj, err := inventoryFreshObject(ctx, d, v.src)
	if err != nil || !inventorySame(*current.Source, inventorySource(obj, v.Relative, current.Source.HashVerified)) {
		return errors.New("来源证据已变化，请重新盘点复核")
	}
	return nil
}
func inventoryStatistics(objects []nasInventoryObject) gin.H {
	sides := gin.H{}
	for _, side := range []string{"bitqiu", "nas"} {
		files, dirs, unknown := 0, 0, 0
		total := int64(0)
		types := map[string]int{}
		duplicates := map[string][]string{}
		for _, obj := range objects {
			if obj.Side != side {
				continue
			}
			if obj.IsDir {
				dirs++
				continue
			}
			files++
			types[strings.ToLower(path.Ext(obj.Path))]++
			if obj.SizeKnown && obj.Size <= int64(^uint64(0)>>1)-total {
				total += obj.Size
			} else {
				unknown++
			}
			if hash := obj.Hashes["sha256"]; hash != "" {
				duplicates[hash] = append(duplicates[hash], obj.Path)
			}
		}
		groups := [][]string{}
		for _, paths := range duplicates {
			if len(paths) > 1 {
				sort.Strings(paths)
				groups = append(groups, paths)
			}
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
		sides[side] = gin.H{"files": files, "directories": dirs, "known_bytes": total, "unknown_size": unknown, "types": types, "duplicate_candidates": groups}
	}
	return sides
}

// A preflight request refreshes each parent once, so N selected files in one
// paginated folder do not trigger N full remote enumerations. A new final
// confirmation always receives a fresh request-local cache.
type inventoryValidationKey struct{}
type inventoryListing struct {
	objects []model.Obj
	err     error
}

func inventoryValidationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, inventoryValidationKey{}, map[string]inventoryListing{})
}

// One complete refreshed parent listing, never a previous request's cache.
func inventoryFreshObject(ctx context.Context, d driver.Driver, src string) (model.Obj, error) {
	cache, _ := ctx.Value(inventoryValidationKey{}).(map[string]inventoryListing)
	key := d.GetStorage().MountPath + "\x00" + path.Dir(src)
	listing, found := cache[key]
	if !found {
		listing.objects, listing.err = op.List(ctx, d, path.Dir(src), model.ListArgs{Refresh: true, NoUpdateIndex: true, OnPage: func(_ []model.Obj) error { return ctx.Err() }})
		if cache != nil {
			cache[key] = listing
		}
	}
	if listing.err != nil {
		return nil, listing.err
	}
	for _, obj := range listing.objects {
		if obj.GetName() == path.Base(src) {
			return obj, nil
		}
	}
	return nil, errors.New("来源不存在")
}
