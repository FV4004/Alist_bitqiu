package handles

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/alist-org/alist/v3/internal/db"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const nasScanJournal = "bitqiu_nas_scans_v1"

// One recent task and one last complete result per scope; no account state is stored.
type nasScanStoredItem struct {
	Item     nasCompareItem `json:"item"`
	Source   string         `json:"source"`
	ObjectID string         `json:"object_id"`
}
type nasScanResult struct {
	ID                             string    `json:"id"`
	CompletedAt                    time.Time `json:"completed_at"`
	Root, Base, Mount, SourceMount string
	Items                          []nasScanStoredItem  `json:"items"`
	Issues                         []nasCompareIssue    `json:"issues"`
	Inventory                      []nasInventoryObject `json:"inventory,omitempty"`
}
type nasScanJob struct {
	Options     nasInventoryOptions `json:"options"`
	Partial     *nasScanResult      `json:"-"`
	ID          string              `json:"id"`
	Key         string              `json:"-"`
	Owner       uint                `json:"-"`
	Source      string              `json:"source"`
	Destination string              `json:"destination"`
	Names       []string            `json:"names"`
	State       string              `json:"state"`
	Stage       string              `json:"stage"`
	Discovered  int                 `json:"discovered"`
	Processed   int                 `json:"processed"`
	Total       int                 `json:"total"`
	Percent     int                 `json:"percent"`
	Current     string              `json:"current"`
	StartedAt   time.Time           `json:"started_at"`
	FinishedAt  *time.Time          `json:"finished_at"`
	Message     string              `json:"message"`
	Result      *nasScanResult      `json:"-"`
	cancel      context.CancelFunc
}
type nasScanDiskJob struct {
	Job     nasScanJob     `json:"job"`
	Owner   uint           `json:"owner"`
	Key     string         `json:"key"`
	Result  *nasScanResult `json:"result"`
	Partial *nasScanResult `json:"partial,omitempty"`
}

var nasScanStore = struct {
	sync.Mutex
	loaded bool
	jobs   map[string]*nasScanJob
}{jobs: map[string]*nasScanJob{}}

func scanRunning(state string) bool {
	return state == "enumerating" || state == "checking" || state == "canceling"
}
func comparisonSourceMount(s *nasComparison) string {
	if s.sourceMount != "" {
		return s.sourceMount
	}
	return s.source.GetStorage().MountPath
}
func scanScope(user *model.User, source, destination string, names []string) (string, []string, error) {
	if len(names) == 0 || len(names) > compareLimit {
		return "", nil, errors.New("请在左侧勾选 1 至 50000 个文件或文件夹")
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, name := range names {
		if utils.ValidateNameComponent(name) != nil {
			return "", nil, errors.New("扫描范围包含无效名称")
		}
		if !seen[name] {
			seen[name] = true
			normalized = append(normalized, name)
		}
	}
	sort.Strings(normalized)
	raw, _ := json.Marshal([]any{user.ID, path.Clean(source), path.Clean(destination), normalized})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), normalized, nil
}

// All journal access is serialized. Failed reads never overwrite an old journal.
func loadNASScansLocked() error {
	if nasScanStore.loaded {
		return nil
	}
	if db.GetDb() == nil {
		return errors.New("扫描数据库尚未就绪")
	}
	var item model.TaskItem
	err := db.GetDb().Where("key = ?", nasScanJournal).First(&item).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var records []nasScanDiskJob
	if err == nil && item.PersistData != "" {
		if err = json.Unmarshal([]byte(item.PersistData), &records); err != nil {
			return errors.New("扫描缓存无法读取，请检查数据库备份；未覆盖原缓存")
		}
	}
	jobs := map[string]*nasScanJob{}
	for _, r := range records {
		j := r.Job
		j.Key = r.Key
		j.Owner = r.Owner
		j.Result = r.Result
		j.Partial = r.Partial
		if scanRunning(j.State) {
			j.State = "interrupted"
			j.Message = "上次扫描因服务重启中断，以下为最近一次完整扫描缓存"
			now := time.Now()
			j.FinishedAt = &now
		}
		jobs[j.Key] = &j
	}
	nasScanStore.jobs = jobs
	nasScanStore.loaded = true
	return nil
}
func saveNASScansLocked() error {
	records := make([]nasScanDiskJob, 0, len(nasScanStore.jobs))
	for _, j := range nasScanStore.jobs {
		records = append(records, nasScanDiskJob{Job: *j, Owner: j.Owner, Key: j.Key, Result: j.Result, Partial: j.Partial})
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return err
	}
	var existing model.TaskItem
	err = db.GetDb().Where("key = ?", nasScanJournal).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.GetDb().Create(&model.TaskItem{Key: nasScanJournal, PersistData: string(raw)}).Error
	}
	if err != nil {
		return err
	}
	return db.GetDb().Model(&model.TaskItem{}).Where("key = ?", nasScanJournal).Update("persist_data", string(raw)).Error
}
func nasScanViewLocked(j *nasScanJob, includeResult bool) gin.H {
	view := gin.H{"job": j, "result": nil}
	// Explicit copy: JSON encoding happens after unlocking, never expose a live job.
	job := *j
	job.Names = append([]string(nil), j.Names...)
	view["job"] = &job
	if j.Partial != nil {
		items := []nasCompareItem{}
		for _, v := range j.Partial.Items {
			items = append(items, v.Item)
		}
		view["attempt"] = gin.H{"scan_id": j.Partial.ID, "items": items, "issues": j.Partial.Issues, "completed_at": j.Partial.CompletedAt, "source": j.Source, "destination": j.Destination, "names": j.Names, "inventory": j.Partial.Inventory, "options": j.Options}
	}
	if includeResult && j.Result != nil {
		items := make([]nasCompareItem, 0, len(j.Result.Items))
		for _, v := range j.Result.Items {
			items = append(items, v.Item)
		}
		view["result"] = gin.H{"scan_id": j.Result.ID, "completed_at": j.Result.CompletedAt, "source": j.Source, "destination": j.Destination, "names": j.Names, "items": items, "issues": j.Result.Issues, "cached": j.Result.ID != j.ID, "options": j.Options, "statistics": inventoryStatistics(j.Result.Inventory), "inventory": j.Result.Inventory}
	}
	return view
}
func BitQiuNASScanStart(c *gin.Context) {
	var req struct {
		SrcDir  string              `json:"src_dir"`
		DstDir  string              `json:"dst_dir"`
		Names   []string            `json:"names"`
		Options nasInventoryOptions `json:"options"`
	}
	if c.ShouldBindJSON(&req) != nil {
		common.ErrorStrResp(c, "扫描参数不合法", 400)
		return
	}
	normalizeInventoryOptions(&req.Options)
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
	key, names, err := scanScope(user, virtual, destination, req.Names)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Options.Enabled {
		key = inventoryDigest([]any{key, req.Options})
	}
	root, rootPath, base, mount, err := compareTarget(destination)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	root.Close()
	// Fail quickly for unreadable individual selections before accepting a task.
	for _, name := range names {
		if !common.CanReadPathByRole(user, path.Join(virtual, name)) {
			common.ErrorStrResp(c, "扫描范围包含无权访问的项目", 403)
			return
		}
	}
	nasScanStore.Lock()
	if err = loadNASScansLocked(); err != nil {
		nasScanStore.Unlock()
		common.ErrorResp(c, err, 500)
		return
	}
	old := nasScanStore.jobs[key]
	if old != nil && scanRunning(old.State) {
		v := nasScanViewLocked(old, true)
		nasScanStore.Unlock()
		common.SuccessResp(c, v)
		return
	}
	active := 0
	for _, j := range nasScanStore.jobs {
		if scanRunning(j.State) {
			active++
		}
	}
	if active >= 2 {
		nasScanStore.Unlock()
		common.ErrorStrResp(c, "已有两个后台扫描，请等待或取消后重试", 429)
		return
	}
	if len(nasScanStore.jobs) >= 12 && old == nil {
		var oldest *nasScanJob
		for _, j := range nasScanStore.jobs {
			if !scanRunning(j.State) && (oldest == nil || j.StartedAt.Before(oldest.StartedAt)) {
				oldest = j
			}
		}
		if oldest != nil {
			delete(nasScanStore.jobs, oldest.Key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	copyUser := *user
	copyUser.Role = append(model.Roles(nil), user.Role...)
	job := &nasScanJob{Options: req.Options, ID: rand.Text(), Key: key, Owner: user.ID, Source: virtual, Destination: destination, Names: names, State: "enumerating", Stage: "enumerating", Percent: -1, StartedAt: time.Now(), cancel: cancel}
	if old != nil {
		job.Result = old.Result
	}
	nasScanStore.jobs[key] = job
	if err = saveNASScansLocked(); err != nil {
		if old == nil {
			delete(nasScanStore.jobs, key)
		} else {
			nasScanStore.jobs[key] = old
		}
		nasScanStore.Unlock()
		cancel()
		common.ErrorStrResp(c, "扫描状态保存失败，未启动后台任务", 500)
		return
	}
	view := nasScanViewLocked(job, true)
	nasScanStore.Unlock()
	s := &nasComparison{owner: user.ID, user: &copyUser, source: source, sourceMount: source.GetStorage().MountPath, sourceVirtual: virtual, destination: destination, root: rootPath, targetBase: base, targetMount: mount, items: []nasCompareItem{}, issues: []nasCompareIssue{}, inventory: map[string][]model.Obj{}, inventoryErrors: map[string]bool{}}
	go runNASScan(ctx, job, s, actual, cancel)
	common.SuccessResp(c, view)
}
func BitQiuNASScanStatus(c *gin.Context) {
	var req struct {
		ID          string              `json:"job_id"`
		Latest      bool                `json:"latest"`
		SrcDir      string              `json:"src_dir"`
		DstDir      string              `json:"dst_dir"`
		Names       []string            `json:"names"`
		KnownResult string              `json:"known_result"`
		Options     nasInventoryOptions `json:"options"`
	}
	if c.ShouldBindJSON(&req) != nil {
		common.ErrorStrResp(c, "扫描查询参数不合法", 400)
		return
	}
	normalizeInventoryOptions(&req.Options)
	user := c.MustGet("user").(*model.User)
	key := ""
	if req.ID == "" && !req.Latest {
		source, err := user.JoinPath(req.SrcDir)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		dst, err := user.JoinPath(req.DstDir)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		key, _, err = scanScope(user, source, dst, req.Names)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
	}
	if key != "" && req.Options.Enabled {
		key = inventoryDigest([]any{key, req.Options})
	}
	nasScanStore.Lock()
	defer nasScanStore.Unlock()
	if err := loadNASScansLocked(); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	var found *nasScanJob
	for _, j := range nasScanStore.jobs {
		if j.Owner != user.ID {
			continue
		}
		if (req.ID != "" && j.ID == req.ID) || (key != "" && j.Key == key) || (req.Latest && (found == nil || j.StartedAt.After(found.StartedAt))) {
			found = j
			if !req.Latest {
				break
			}
		}
	}
	if found == nil {
		common.SuccessResp(c, gin.H{"job": nil, "result": nil})
		return
	}
	if !common.CanReadPathByRole(user, found.Source) || !common.CanReadPathByRole(user, found.Destination) {
		common.ErrorStrResp(c, "扫描路径权限已变化", 403)
		return
	}
	view := nasScanViewLocked(found, found.Result == nil || req.KnownResult != found.Result.ID)
	common.SuccessResp(c, view)
}
func BitQiuNASScanCancel(c *gin.Context) {
	var req struct {
		ID string `json:"job_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		common.ErrorStrResp(c, "请选择扫描任务", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	nasScanStore.Lock()
	defer nasScanStore.Unlock()
	if err := loadNASScansLocked(); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	for _, j := range nasScanStore.jobs {
		if j.ID == req.ID && j.Owner == user.ID {
			if scanRunning(j.State) && j.cancel != nil {
				j.State = "canceling"
				j.Message = "正在取消后台扫描"
				j.cancel()
			}
			common.SuccessResp(c, nasScanViewLocked(j, true))
			return
		}
	}
	common.ErrorStrResp(c, "扫描任务不存在", 404)
}
func enumerateNASScan(ctx context.Context, j *nasScanJob, s *nasComparison, actual string) (int, error) {
	if j.Options.Enabled {
		ctx = inventoryValidationContext(ctx)
	}
	discovered := 0
	ancestors := map[string]bool{}
	update := func(rel string) {
		nasScanStore.Lock()
		j.Discovered = discovered
		j.Current = rel
		nasScanStore.Unlock()
	}
	var walk func(string, string, []model.Obj, int) error
	walk = func(src, rel string, children []model.Obj, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 128 {
			return errors.New("扫描目录深度超过 128，请缩小范围")
		}
		s.inventory[src] = children
		names := map[string]bool{}
		for _, obj := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			discovered++
			if discovered > compareLimit {
				return errors.New("扫描超过 50000 项，请缩小范围")
			}
			update(path.Join(rel, obj.GetName()))
			if utils.ValidateNameComponent(obj.GetName()) != nil || names[obj.GetName()] {
				return errors.New("网盘返回无效或重复名称，请刷新后重试")
			}
			names[obj.GetName()] = true
			childPath := path.Join(src, obj.GetName())
			relative := path.Join(rel, obj.GetName())
			if !common.CanReadPathByRole(s.user, path.Join(s.sourceVirtual, relative)) {
				s.issues = append(s.issues, nasCompareIssue{relative, "来源无读取权限"})
				continue
			}
			if !obj.IsDir() {
				continue
			}
			id := obj.GetID()
			if id == "" {
				id = childPath
			}
			if ancestors[id] {
				s.inventoryErrors[childPath] = true
				continue
			}
			ancestors[id] = true
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			nested, err := op.List(ctx, s.source, childPath, model.ListArgs{Refresh: true, NoUpdateIndex: true, OnPage: func(_ []model.Obj) error { update(relative); return ctx.Err() }})
			if err != nil {
				s.inventoryErrors[childPath] = true
				delete(ancestors, id)
				continue
			}
			if err = walk(childPath, relative, nested, depth+1); err != nil {
				return err
			}
			delete(ancestors, id)
		}
		return nil
	}
	selected := []model.Obj{}
	for _, name := range j.Names {
		var obj model.Obj
		var err error
		if j.Options.Enabled {
			obj, err = inventoryFreshObject(ctx, s.source, path.Join(actual, name))
		} else {
			obj, err = op.Get(ctx, s.source, path.Join(actual, name))
		}
		if err != nil {
			return 0, errors.New("所选来源已改变或无法读取，请刷新目录后重试")
		}
		selected = append(selected, obj)
	}
	err := walk(actual, "", selected, 0)
	return discovered, err
}
func runNASScan(ctx context.Context, j *nasScanJob, s *nasComparison, actual string, cancel context.CancelFunc) {
	defer cancel()
	// Panic must never crash the service or leave a task permanently active.
	var finalErr error
	defer func() {
		if v := recover(); v != nil {
			finalErr = errors.New("扫描发生异常，请重试并检查服务日志")
		}
		nasScanStore.Lock()
		defer nasScanStore.Unlock()
		now := time.Now()
		j.FinishedAt = &now
		j.cancel = nil
		if finalErr != nil || ctx.Err() != nil {
			j.State = "failed"
			j.Message = "扫描未完成：" + fmt.Sprint(finalErr)
			if errors.Is(ctx.Err(), context.Canceled) {
				j.State = "canceled"
				j.Message = "扫描已取消，以下为最近一次完整扫描缓存"
			} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				j.State = "timed_out"
				j.Message = "扫描超时，以下为最近一次完整扫描缓存"
			}
		}
		if err := saveNASScansLocked(); err != nil {
			j.State = "failed"
			j.Message = "扫描状态保存失败，请检查数据库；原缓存未覆盖"
		}
	}()
	_, finalErr = enumerateNASScan(ctx, j, s, actual)
	if finalErr != nil {
		return
	}
	total := 0
	for _, children := range s.inventory {
		total += len(children)
	}
	nasScanStore.Lock()
	j.State = "checking"
	j.Stage = "checking"
	j.Total = total
	j.Percent = 0
	nasScanStore.Unlock()
	root, rootPath, base, mount, err := compareTarget(s.destination)
	if err != nil {
		finalErr = err
		return
	}
	defer root.Close()
	if rootPath != s.root || base != s.targetBase || mount != s.targetMount {
		finalErr = errors.New("NAS 存储挂载已改变，请重新扫描")
		return
	}
	scan := func(emit func(string, int) error) error { return scanNASComparison(ctx, s, actual, root, emit) }
	if j.Options.Enabled {
		scan = func(emit func(string, int) error) error { return scanNASInventory(ctx, j, s, actual, root, emit) }
	}
	finalErr = scan(func(rel string, n int) error {
		nasScanStore.Lock()
		j.Current = rel
		j.Processed = n
		if total > 0 {
			j.Percent = min(99, n*100/total)
		}
		nasScanStore.Unlock()
		return ctx.Err()
	})
	if finalErr != nil || ctx.Err() != nil {
		return
	}
	result := &nasScanResult{ID: j.ID, CompletedAt: time.Now(), Root: s.root, Base: s.targetBase, Mount: s.targetMount, SourceMount: s.sourceMount, Items: []nasScanStoredItem{}, Issues: s.issues, Inventory: s.inventoryObjects}
	for _, v := range s.items {
		result.Items = append(result.Items, nasScanStoredItem{v, v.src, v.ObjectID})
	}
	nasScanStore.Lock()
	count := len(result.Items)
	for key, other := range nasScanStore.jobs {
		if key != j.Key && other.Result != nil {
			count += len(other.Result.Items)
		}
	}
	if count > 200000 {
		nasScanStore.Unlock()
		finalErr = errors.New("扫描缓存超过 200000 项，请缩小范围后重试")
		return
	}
	oldResult := j.Result
	carryInventoryReviews(result, oldResult)
	j.Result = result
	j.State = "completed"
	j.Stage = "completed"
	j.Message = "扫描完成"
	// Incomplete branches must not replace a known complete result.
	if len(s.issues) > 0 {
		j.State = "partial"
		j.Message = "本次扫描存在未完成分支，以下为最近一次完整扫描缓存"
		j.Result = oldResult
		j.Partial = result
	} else {
		j.Partial = nil
		j.Percent = 100
		j.Processed = total
	}
	if err = saveNASScansLocked(); err != nil {
		j.Result = oldResult
		finalErr = errors.New("扫描结果保存失败，旧缓存已保留")
	}
	nasScanStore.Unlock()
}
func restoreNASScan(user *model.User, id string) *nasComparison {
	nasScanStore.Lock()
	defer nasScanStore.Unlock()
	if loadNASScansLocked() != nil {
		return nil
	}
	for _, j := range nasScanStore.jobs {
		if j.Owner != user.ID || j.Result == nil || j.Result.ID != id {
			continue
		}
		r := j.Result
		s := &nasComparison{owner: user.ID, user: user, expires: time.Now().Add(15 * time.Minute), sourceVirtual: j.Source, destination: j.Destination, root: r.Root, targetBase: r.Base, targetMount: r.Mount, sourceMount: r.SourceMount, items: []nasCompareItem{}, issues: r.Issues}
		for _, v := range r.Items {
			item := v.Item
			item.src = v.Source
			item.ObjectID = v.ObjectID
			s.items = append(s.items, item)
		}
		return s
	}
	return nil
}
