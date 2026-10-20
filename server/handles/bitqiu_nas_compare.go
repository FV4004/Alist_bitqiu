package handles

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	"golang.org/x/text/unicode/norm"
)

const compareLimit = 50000

var compareSlots = make(chan struct{}, 2)

type nasCompareItem struct {
	ID         string   `json:"id"`
	Relative   string   `json:"relative"`
	IsDir      bool     `json:"is_dir"`
	Category   string   `json:"category"`
	Size       int64    `json:"size"`
	Candidates []string `json:"candidates,omitempty"`
	Blocked    bool     `json:"blocked"`
	obj        model.Obj
	src        string
}
type nasCompareIssue struct {
	Relative string `json:"relative"`
	Message  string `json:"message"`
}
type nasComparison struct {
	owner                                                     uint
	user                                                      *model.User
	expires                                                   time.Time
	source                                                    driver.Driver
	sourceVirtual, destination, root, targetBase, targetMount string
	items                                                     []nasCompareItem
	issues                                                    []nasCompareIssue
}
type nasComparePlan struct {
	mu      sync.Mutex
	owner   uint
	expires time.Time
	records []fs.NASRecord
	used    bool
}

var nasComparisons = struct {
	sync.Mutex
	scans map[string]*nasComparison
	plans map[string]*nasComparePlan
}{scans: map[string]*nasComparison{}, plans: map[string]*nasComparePlan{}}

func compareFold(s string) string { return strings.ToLower(norm.NFKC.String(s)) }
func compareSimilar(a, b string) bool {
	a, b = compareFold(a), compareFold(b)
	if a == b {
		return true
	}
	// Preserve all digit sequences: different seasons/episodes are not rename candidates.
	// Removing non-digits here deliberately keeps digit runs separate.
	runs := func(s string) string {
		var out strings.Builder
		in := false
		for _, r := range s {
			if r >= '0' && r <= '9' {
				out.WriteRune(r)
				in = true
			} else if in {
				out.WriteByte('|')
				in = false
			}
		}
		return out.String()
	}
	if runs(a) != runs(b) || path.Ext(a) != path.Ext(b) {
		return false
	}
	normalize := func(s string) string {
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(" -_().[]", r) {
				return -1
			}
			return r
		}, s)
	}
	a, b = normalize(a), normalize(b)
	if a == b {
		return true
	}
	// Bounded edit distance (one character only); a suggestion never means equality.
	x, y := []rune(a), []rune(b)
	if len(x) < 4 || len(y) < 4 || len(x)-len(y) > 1 || len(y)-len(x) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(x) && j < len(y) {
		if x[i] == y[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if len(x) >= len(y) {
			i++
		}
		if len(y) >= len(x) {
			j++
		}
	}
	return edits+(len(x)-i)+(len(y)-j) <= 1
}
func cleanComparisons() {
	now := time.Now()
	for k, s := range nasComparisons.scans {
		if now.After(s.expires) {
			delete(nasComparisons.scans, k)
		}
	}
	for k, p := range nasComparisons.plans {
		if now.After(p.expires) {
			delete(nasComparisons.plans, k)
		}
	}
}
func compareTarget(destination string) (*os.Root, string, string, string, error) {
	d, _, err := localNASDirectory(destination)
	if err != nil {
		return nil, "", "", "", err
	}
	provider, ok := d.GetAddition().(driver.IRootPath)
	if !ok {
		return nil, "", "", "", errors.New("本地存储缺少根目录")
	}
	st, actual, err := op.GetStorageAndActualPath(destination)
	if err != nil {
		return nil, "", "", "", err
	}
	rel := strings.TrimPrefix(path.Clean(actual), "/")
	if rel == "" {
		rel = "."
	}
	root, err := os.OpenRoot(provider.GetRootPath())
	if err != nil {
		return nil, "", "", "", err
	}
	if err = fs.NASCheckParents(root, path.Join(rel, ".compare-check")); err != nil {
		root.Close()
		return nil, "", "", "", err
	}
	return root, provider.GetRootPath(), rel, st.GetStorage().MountPath, nil
}
func scanNASComparison(ctx context.Context, s *nasComparison, actual string, target *os.Root, emit func(string, int) error) error {
	ancestors := map[string]bool{}
	visited := 0
	issue := func(rel, msg string) { s.issues = append(s.issues, nasCompareIssue{rel, msg}) }
	var walk func(string, string, int, bool, []string) error
	walk = func(src, rel string, depth int, parentMissing bool, inherited []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 128 || visited >= compareLimit {
			return errors.New("扫描达到 50000 项或深度上限，请选择更小目录")
		}
		if err := emit(rel, visited); err != nil {
			return err
		}
		if !common.CanReadPathByRole(s.user, path.Join(s.sourceVirtual, rel)) {
			issue(rel, "目录无读取权限")
			return nil
		}
		args := model.ListArgs{Refresh: true, NoUpdateIndex: true, OnPage: func(_ []model.Obj) error { return emit(rel, visited) }}
		children, err := op.List(ctx, s.source, src, args)
		if err != nil {
			issue(rel, "网盘目录读取失败，未判定此分支缺失")
			return nil
		}
		var local []os.DirEntry
		if !parentMissing {
			if err := fs.NASCheckParents(target, path.Join(s.targetBase, rel, ".compare-check")); err != nil {
				issue(rel, "NAS 路径包含链接或非目录对象")
				return nil
			}
			f, e := target.Open(path.Join(s.targetBase, rel))
			if e != nil {
				issue(rel, "NAS 目录读取失败，未判定此分支缺失")
				return nil
			}
			local, e = f.ReadDir(-1)
			f.Close()
			if e != nil {
				issue(rel, "NAS 目录未完整读取，未判定此分支缺失")
				return nil
			}
		}
		names := map[string]bool{}
		for _, obj := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			visited++
			if visited > compareLimit {
				return errors.New("扫描超过 50000 项，请缩小范围")
			}
			name := obj.GetName()
			if utils.ValidateNameComponent(name) != nil || names[name] {
				issue(rel, "网盘返回无效或重复名称，此分支需重新核对")
				continue
			}
			names[name] = true
			relative := path.Join(rel, name)
			if !common.CanReadPathByRole(s.user, path.Join(s.sourceVirtual, relative)) || !common.CanReadPathByRole(s.user, path.Join(s.destination, relative)) {
				issue(relative, "路径无读取权限")
				continue
			}
			targetPath := path.Join(s.targetBase, relative)
			info, e := target.Lstat(targetPath)
			if e == nil {
				if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) || info.IsDir() != obj.IsDir() {
					issue(relative, "同路径类型冲突或链接，已阻止下载")
					continue
				}
				if !obj.IsDir() {
					continue
				}
			} else if !os.IsNotExist(e) {
				issue(relative, "NAS 对象无法检查，未判定缺失")
				continue
			}
			missing := os.IsNotExist(e)
			candidates := append([]string(nil), inherited...)
			if missing && !parentMissing {
				for candidateIndex, candidate := range local {
					if candidateIndex >= 512 {
						break
					}
					if !common.CanReadPathByRole(s.user, path.Join(s.destination, rel, candidate.Name())) {
						continue
					}
					if len(candidates) >= 5 {
						break
					}
					if candidate.Type()&os.ModeSymlink != 0 || candidate.IsDir() != obj.IsDir() {
						continue
					}
					if compareSimilar(name, candidate.Name()) {
						candidates = append(candidates, path.Join(rel, candidate.Name()))
					}
				}
			}
			if missing {
				category := "files"
				if obj.IsDir() {
					category = "folders"
				}
				if len(candidates) > 0 {
					category = "review"
				}
				size := int64(-1)
				if !obj.IsDir() && model.ObjectSizeKnown(obj) {
					size = obj.GetSize()
				}
				s.items = append(s.items, nasCompareItem{ID: rand.Text(), Relative: relative, IsDir: obj.IsDir(), Category: category, Size: size, Candidates: candidates, obj: obj, src: path.Join(src, name)})
			}
			if obj.IsDir() {
				key := obj.GetID()
				if key == "" {
					key = path.Join(src, name)
				}
				if ancestors[key] {
					issue(relative, "检测到目录循环，未扫描此分支")
					continue
				}
				ancestors[key] = true
				timer := time.NewTimer(150 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				if err := walk(path.Join(src, name), relative, depth+1, missing, candidates); err != nil {
					return err
				}
				delete(ancestors, key)
			}
		}
		return nil
	}
	if err := walk(actual, "", 0, false, nil); err != nil {
		return err
	}
	for i := range s.items {
		if !s.items[i].IsDir {
			continue
		}
		for _, p := range s.issues {
			if p.Relative == "" || p.Relative == s.items[i].Relative || strings.HasPrefix(p.Relative, s.items[i].Relative+"/") {
				s.items[i].Blocked = true
				break
			}
		}
	}
	return nil
}
func BitQiuNASCompare(c *gin.Context) {
	var req struct {
		SrcDir string `json:"src_dir"`
		DstDir string `json:"dst_dir"`
	}
	if c.ShouldBindJSON(&req) != nil {
		common.ErrorStrResp(c, "请选择两个目录", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	source, actual, virtual, err := nasSelectionSource(user, req.SrcDir)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	destination, err := user.JoinPath(req.DstDir)
	if err != nil || !common.CanReadPathByRole(user, destination) {
		common.ErrorStrResp(c, "无权访问 NAS 目录", 403)
		return
	}
	root, rootPath, base, mount, err := compareTarget(destination)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	defer root.Close()
	select {
	case compareSlots <- struct{}{}:
		defer func() { <-compareSlots }()
	default:
		common.ErrorStrResp(c, "扫描繁忙，请稍后重试", 429)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Accel-Buffering", "no")
	emit := func(v any) error {
		if err := json.NewEncoder(c.Writer).Encode(v); err != nil {
			return err
		}
		c.Writer.Flush()
		return ctx.Err()
	}
	s := &nasComparison{user: user, owner: user.ID, expires: time.Now().Add(15 * time.Minute), source: source, sourceVirtual: virtual, destination: destination, root: rootPath, targetBase: base, targetMount: mount, items: []nasCompareItem{}, issues: []nasCompareIssue{}}
	err = scanNASComparison(ctx, s, actual, root, func(rel string, n int) error { return emit(gin.H{"event": "progress", "relative": rel, "visited": n}) })
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = emit(gin.H{"event": "error", "message": err.Error()})
		return
	}
	s.expires = time.Now().Add(15 * time.Minute)
	nasComparisons.Lock()
	cleanComparisons()
	total := len(s.items)
	for _, v := range nasComparisons.scans {
		total += len(v.items)
	}
	if len(nasComparisons.scans) >= 8 || total > 200000 {
		nasComparisons.Unlock()
		_ = emit(gin.H{"event": "error", "message": "扫描快照繁忙，请稍后重试"})
		return
	}
	id := rand.Text()
	nasComparisons.scans[id] = s
	nasComparisons.Unlock()
	_ = emit(gin.H{"event": "done", "scan_id": id, "source": req.SrcDir, "destination": req.DstDir, "items": s.items, "issues": s.issues, "expires_at": s.expires})
}

type nasCompareSkip struct {
	Relative string `json:"relative"`
	Reason   string `json:"reason"`
}
type nasCompareTask struct {
	ID       string `json:"id"`
	Relative string `json:"relative"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
}

func compareOccupied() map[string]bool {
	out := map[string]bool{}
	for _, v := range fs.NASDownloads() {
		if v.State != "completed" && v.State != "canceled" {
			out[v.Root+"\x00"+v.Relative] = true
		}
	}
	return out
}
func compareRecordMissing(r fs.NASRecord) error {
	root, err := os.OpenRoot(r.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := fs.NASCheckParents(root, r.Relative); err != nil {
		return err
	}
	_, err = root.Lstat(r.Relative)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		return errors.New("NAS 中已存在，跳过")
	}
	return errors.New("NAS 目标无法检查，跳过")
}
func BitQiuNASComparePrepare(c *gin.Context) {
	var req struct {
		ScanID string   `json:"scan_id"`
		IDs    []string `json:"ids"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 || len(req.IDs) > compareLimit {
		common.ErrorStrResp(c, "请选择需要补缺的项目", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	nasComparisons.Lock()
	cleanComparisons()
	s := nasComparisons.scans[req.ScanID]
	nasComparisons.Unlock()
	if s == nil || s.owner != user.ID {
		common.ErrorStrResp(c, "扫描快照已过期，请重新扫描", 409)
		return
	}
	selected := map[string]nasCompareItem{}
	byID := map[string]nasCompareItem{}
	for _, v := range s.items {
		byID[v.ID] = v
	}
	for _, id := range req.IDs {
		v, ok := byID[id]
		if !ok || v.Blocked {
			common.ErrorStrResp(c, "选择项无效或包含未扫描完整的目录", 400)
			return
		}
		selected[id] = v
	}
	// Revalidate the mounts and role permissions before constructing real task records.
	source, _, _, err := nasSelectionSource(user, s.sourceVirtual)
	if err != nil || source.GetStorage().MountPath != s.source.GetStorage().MountPath {
		common.ErrorStrResp(c, "来源存储已变化，请重新扫描", 409)
		return
	}
	root, rootPath, base, mount, err := compareTarget(s.destination)
	if err != nil {
		common.ErrorResp(c, err, 409)
		return
	}
	root.Close()
	if rootPath != s.root || base != s.targetBase || mount != s.targetMount || !common.CanReadPathByRole(user, s.destination) {
		common.ErrorStrResp(c, "目标存储或权限已变化，请重新扫描", 409)
		return
	}
	records := []fs.NASRecord{}
	skipped := []nasCompareSkip{}
	occupied := compareOccupied()
	dedup := map[string]bool{}
	batch := rand.Text()
	groups := map[string]string{}
	for _, v := range s.items {
		direct := false
		_, direct = selected[v.ID]
		include, reviewAllowed := direct, direct
		for _, parent := range selected {
			if parent.IsDir && strings.HasPrefix(v.Relative, parent.Relative+"/") {
				include = true
				if parent.Category == "review" {
					reviewAllowed = true
				}
			}
		}
		if !include {
			continue
		}
		if v.Blocked {
			skipped = append(skipped, nasCompareSkip{v.Relative, "目录扫描不完整"})
			continue
		}
		if v.Category == "review" && !reviewAllowed {
			skipped = append(skipped, nasCompareSkip{v.Relative, "疑似项需在待核对页明确选择"})
			continue
		}
		srcVirtual := path.Join(s.sourceVirtual, v.Relative)
		dstVirtual := path.Join(s.destination, v.Relative)
		if !common.CanReadPathByRole(user, srcVirtual) || !common.CanReadPathByRole(user, dstVirtual) {
			skipped = append(skipped, nasCompareSkip{v.Relative, "路径权限不足"})
			continue
		}
		obj, e := op.Get(c.Request.Context(), source, v.src)
		if e != nil || obj.IsDir() != v.IsDir || (v.obj.GetID() != "" && obj.GetID() != v.obj.GetID()) {
			skipped = append(skipped, nasCompareSkip{v.Relative, "来源已改变，请重新扫描"})
			continue
		}
		relative := path.Join(base, v.Relative)
		record, e := fs.NewNASRecord(user, srcVirtual, dstVirtual, rootPath, relative, source.GetStorage().MountPath, mount, v.src, obj)
		if e != nil {
			common.ErrorResp(c, e, 400)
			return
		}
		key := record.Root + "\x00" + record.Relative
		if dedup[key] {
			continue
		}
		dedup[key] = true
		if occupied[key] {
			skipped = append(skipped, nasCompareSkip{v.Relative, "已有未完成、暂停或失败任务，请操作原任务"})
			continue
		}
		if e = compareRecordMissing(record); e != nil {
			skipped = append(skipped, nasCompareSkip{v.Relative, e.Error()})
			continue
		}
		top := strings.SplitN(v.Relative, "/", 2)[0]
		if strings.Contains(v.Relative, "/") || v.IsDir {
			if groups[top] == "" {
				groups[top] = "group-" + rand.Text()
			}
			record.GroupID = groups[top]
			record.GroupName = top
		}
		record.BatchID = batch
		if record.IsDir {
			record.TotalBytes = 0
		}
		records = append(records, record)
	}
	// Do not silently merge different paths that may alias on case-folding NAS filesystems.
	aliases := map[string]string{}
	for _, r := range records {
		prefix := ""
		for _, part := range strings.Split(r.Relative, "/") {
			prefix = path.Join(prefix, part)
			k := compareFold(prefix)
			if prev, ok := aliases[k]; ok && prev != prefix {
				common.ErrorStrResp(c, "所选目标存在大小写或全半角路径冲突，请分别复核后选择："+prev+" / "+prefix, 409)
				return
			}
			aliases[k] = prefix
		}
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Relative < records[j].Relative })
	tasks := []nasCompareTask{}
	known := int64(0)
	unknown := 0
	files, dirs := 0, 0
	for _, r := range records {
		rel := strings.TrimPrefix(strings.TrimPrefix(r.Destination, s.destination), "/")
		tasks = append(tasks, nasCompareTask{r.ID, rel, r.IsDir, r.TotalBytes})
		if r.IsDir {
			dirs++
		} else {
			files++
			if r.TotalBytes >= 0 && r.TotalBytes <= math.MaxInt64-known {
				known += r.TotalBytes
			} else {
				unknown++
			}
		}
	}
	plan := &nasComparePlan{owner: user.ID, expires: time.Now().Add(15 * time.Minute), records: records}
	id := rand.Text()
	nasComparisons.Lock()
	cleanComparisons()
	total := len(records)
	for _, v := range nasComparisons.plans {
		total += len(v.records)
	}
	if len(nasComparisons.plans) >= 8 || total > 200000 {
		nasComparisons.Unlock()
		common.ErrorStrResp(c, "确认清单繁忙，请稍后重试", 429)
		return
	}
	nasComparisons.plans[id] = plan
	nasComparisons.Unlock()
	common.SuccessResp(c, gin.H{"plan_id": id, "source": s.sourceVirtual, "destination": s.destination, "tasks": tasks, "skipped": skipped, "files": files, "directories": dirs, "known_bytes": known, "unknown_count": unknown, "expires_at": plan.expires})
}
func BitQiuNASCompareCommit(c *gin.Context) {
	var req struct {
		PlanID    string `json:"plan_id"`
		Confirmed bool   `json:"confirmed"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirmed {
		common.ErrorStrResp(c, "请人工确认最终去重清单", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	nasComparisons.Lock()
	plan := nasComparisons.plans[req.PlanID]
	nasComparisons.Unlock()
	if plan == nil || plan.owner != user.ID {
		common.ErrorStrResp(c, "确认清单已失效", 409)
		return
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.used || time.Now().After(plan.expires) {
		common.ErrorStrResp(c, "确认清单已使用或过期", 409)
		return
	}
	records := []fs.NASRecord{}
	skipped := []nasCompareSkip{}
	occupied := compareOccupied()
	for _, r := range plan.records {
		if !common.CanReadPathByRole(user, r.Source) || !common.CanReadPathByRole(user, r.Destination) {
			common.ErrorStrResp(c, "路径权限已变化，请重新扫描", 403)
			return
		}
		if occupied[r.Root+"\x00"+r.Relative] {
			skipped = append(skipped, nasCompareSkip{r.Destination, "已有任务，跳过"})
			continue
		}
		if err := compareRecordMissing(r); err != nil {
			skipped = append(skipped, nasCompareSkip{r.Destination, err.Error()})
			continue
		}
		if err := fs.NASValidateTarget(r); err != nil {
			common.ErrorResp(c, err, 409)
			return
		}
		records = append(records, r)
	}
	if len(records) > 0 {
		if err := fs.QueueNASDownloads(records, user); err != nil {
			common.ErrorResp(c, err, 409)
			return
		}
	}
	plan.used = true
	nasComparisons.Lock()
	delete(nasComparisons.plans, req.PlanID)
	nasComparisons.Unlock()
	common.SuccessResp(c, gin.H{"queued": len(records), "skipped": skipped})
}
