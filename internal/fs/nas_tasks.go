package fs

import (
	"context"
	"crypto/rand"
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
	"github.com/alist-org/alist/v3/internal/task"
	"github.com/xhofe/tache"
	"gorm.io/gorm"
)

const nasJournalKey = "bitqiu_nas_v1"

type NASRecord struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Source           string     `json:"source"`
	Destination      string     `json:"destination"`
	CreatorID        uint       `json:"creator_id"`
	State            string     `json:"state"`
	TotalBytes       int64      `json:"total_bytes"`
	TransferredBytes int64      `json:"transferred_bytes"`
	IsDir            bool       `json:"is_dir"`
	Modified         time.Time  `json:"modified"`
	StartTime        *time.Time `json:"start_time"`
	EndTime          *time.Time `json:"end_time"`
	Error            string     `json:"error"`
	CompletionReason string     `json:"completion_reason"`
	RestartRequired  bool       `json:"restart_required"`
	Validator        string     `json:"validator,omitempty"`
	ValidatorKind    string     `json:"validator_kind,omitempty"`
	// Destination root is pinned when the task is created. A storage remount
	// must not silently send an existing task into a different NAS folder.
	Root     string `json:"root"`
	Relative string `json:"relative"`
	SrcMount string `json:"src_mount"`
	DstMount string `json:"dst_mount"`
	SrcPath  string `json:"src_path"`
	Restart  bool   `json:"restart,omitempty"`
}
type NASView struct {
	NASRecord
	Progress  float64 `json:"progress"`
	Speed     float64 `json:"speed_bytes_per_second"`
	CanStart  bool    `json:"can_start"`
	CanPause  bool    `json:"can_pause"`
	CanCancel bool    `json:"can_cancel"`
}
type nasTask struct {
	mu     sync.Mutex
	action sync.Mutex
	record NASRecord
	active bool
	queued bool
	cancel context.CancelFunc
	done   chan struct{}
	meter  task.TransferMeter
}

var nasRegistry = struct {
	sync.Mutex
	tasks map[string]*nasTask
}{tasks: map[string]*nasTask{}}
var nasSaveMu sync.Mutex
var nasHealth = struct {
	sync.Mutex
	err error
}{}

func NASDownloadsAvailable() error {
	nasHealth.Lock()
	defer nasHealth.Unlock()
	if nasHealth.err != nil {
		return errors.New("NAS 任务记录无法读取，请检查容器日志和数据库备份；为保护原记录，暂未启用任务写入")
	}
	return nil
}

var nasCheckpointMu sync.Mutex
var nasCheckpointAt time.Time

func nasTerminal(state string) bool { return state == "completed" || state == "canceled" }
func (n *nasTask) view() NASView {
	n.mu.Lock()
	r := n.record
	n.mu.Unlock()
	meter := n.meter.Snapshot(time.Now())
	v := NASView{NASRecord: r, CanStart: r.State == "waiting" || r.State == "paused" || r.State == "failed", CanPause: r.State == "downloading", CanCancel: !nasTerminal(r.State) && r.State != "canceling"}
	if r.TotalBytes > 0 {
		v.Progress = min(100, float64(r.TransferredBytes)*100/float64(r.TotalBytes))
	}
	if r.State == "completed" {
		v.Progress = 100
	}
	if r.State == "downloading" {
		v.Speed = meter.SpeedBytesPerSecond
	}
	return v
}
func nasFind(id string) (*nasTask, bool) {
	nasRegistry.Lock()
	defer nasRegistry.Unlock()
	n, ok := nasRegistry.tasks[id]
	return n, ok
}
func NASDownloads() []NASView {
	nasRegistry.Lock()
	tasks := make([]*nasTask, 0, len(nasRegistry.tasks))
	for _, n := range nasRegistry.tasks {
		tasks = append(tasks, n)
	}
	nasRegistry.Unlock()
	result := make([]NASView, 0, len(tasks))
	for _, n := range tasks {
		result = append(result, n.view())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func SaveNASDownloads() error {
	if err := NASDownloadsAvailable(); err != nil {
		return err
	}
	nasSaveMu.Lock()
	defer nasSaveMu.Unlock()
	views := NASDownloads()
	records := make([]NASRecord, 0, len(views))
	for _, v := range views {
		records = append(records, v.NASRecord)
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	database := db.GetDb()
	if database == nil {
		return errors.New("任务数据库尚未就绪")
	}
	var item model.TaskItem
	err = database.Where("key = ?", nasJournalKey).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return database.Create(&model.TaskItem{Key: nasJournalKey, PersistData: string(data)}).Error
	}
	if err != nil {
		return err
	}
	return database.Model(&model.TaskItem{}).Where("key = ?", nasJournalKey).Update("persist_data", string(data)).Error
}

// Uses the existing task_items table; NAS state is durable independently of
// the optional legacy copy-task persistence setting. No schema change.
func InitNASDownloads() (result error) {
	nasHealth.Lock()
	nasHealth.err = nil
	nasHealth.Unlock()
	defer func() { nasHealth.Lock(); nasHealth.err = result; nasHealth.Unlock() }()
	var item model.TaskItem
	err := db.GetDb().Where("key = ?", nasJournalKey).First(&item).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var records []NASRecord
	if err == nil && item.PersistData != "" {
		if err = json.Unmarshal([]byte(item.PersistData), &records); err != nil {
			return err
		}
	}
	nasRegistry.Lock()
	nasRegistry.tasks = map[string]*nasTask{}
	for _, r := range records {
		if r.State == "downloading" || r.State == "waiting" || r.State == "pausing" {
			r.State = "paused"
			r.Error = "服务已重启，临时文件已保留，请开始任务继续下载"
		}
		if r.State == "canceling" {
			r.State = "canceled"
			r.CompletionReason = "user_canceled"
		}
		nasRegistry.tasks[r.ID] = &nasTask{record: r}
	}
	nasRegistry.Unlock()
	if err = SaveNASDownloads(); err != nil {
		return err
	}

	return nil
}

// ResetNASDownloads is also useful when an embedding application replaces its
// database; production initialization happens once from InitTaskManager.
func ResetNASDownloads() {
	nasHealth.Lock()
	nasHealth.err = nil
	nasHealth.Unlock()
	nasRegistry.Lock()
	nasRegistry.tasks = map[string]*nasTask{}
	nasRegistry.Unlock()
}
func NewNASRecord(user *model.User, source, destination, root, relative, srcMount, dstMount, srcPath string, obj model.Obj) (NASRecord, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return NASRecord{}, err
	}
	size := obj.GetSize()
	if !model.ObjectSizeKnown(obj) {
		size = -1
	}
	return NASRecord{ID: hex.EncodeToString(token), Name: obj.GetName(), Source: source, Destination: destination, Root: root, Relative: relative, SrcMount: srcMount, DstMount: dstMount, SrcPath: srcPath, CreatorID: user.ID, State: "waiting", TotalBytes: size, IsDir: obj.IsDir(), Modified: obj.ModTime()}, nil
}
func QueueNASDownloads(records []NASRecord, user *model.User) error {
	if err := NASDownloadsAvailable(); err != nil {
		return err
	}
	nasRegistry.Lock()
	for _, r := range records {
		for _, existing := range nasRegistry.tasks {
			existing.mu.Lock()
			occupied := existing.record.Root == r.Root && existing.record.Relative == r.Relative && !nasTerminal(existing.record.State)
			existing.mu.Unlock()
			if occupied {
				nasRegistry.Unlock()
				return fmt.Errorf("该目标已有任务，请开始原任务或选择其他目录：%s", r.Name)
			}
		}
	}
	for _, r := range records {
		nasRegistry.tasks[r.ID] = &nasTask{record: r}
	}
	nasRegistry.Unlock()
	if err := SaveNASDownloads(); err != nil {
		nasRegistry.Lock()
		for _, r := range records {
			delete(nasRegistry.tasks, r.ID)
		}
		nasRegistry.Unlock()
		return err
	}
	for _, r := range records {
		n, _ := nasFind(r.ID)
		enqueueNAS(n, user)
	}
	return nil
}
func enqueueNAS(n *nasTask, user *model.User) {
	n.mu.Lock()
	if n.queued {
		n.mu.Unlock()
		return
	}
	n.queued = true
	r := n.record
	n.mu.Unlock()
	t := &CopyTask{NAS: n, SrcStorageMp: r.SrcMount, DstStorageMp: r.DstMount, SrcObjPath: r.SrcPath, DstDirPath: path.Dir(r.Relative)}
	t.TaskExtension.Creator = user
	t.TaskExtension.Base.ID = r.ID
	CopyTaskManager.Add(t)
}

// Every resumed execution is a fresh queue entry, with the same public task ID.
// The old worker retains its own terminal state and cannot enqueue a duplicate
// or overwrite the new run during the upstream worker's final callbacks.
func ControlNASDownload(ctx context.Context, id, action string, restart bool, user *model.User, pauseWaiting ...bool) (bool, error) {
	if err := NASDownloadsAvailable(); err != nil {
		return false, err
	}
	n, ok := nasFind(id)
	if !ok {
		return false, errors.New("任务不存在")
	}
	n.action.Lock()
	defer n.action.Unlock()
	n.mu.Lock()
	state := n.record.State
	active := n.active
	if action == "start" {
		if state != "waiting" && state != "paused" && state != "failed" {
			n.mu.Unlock()
			return false, nil
		}
		if active {
			n.mu.Unlock()
			return false, nil
		}
		if state == "waiting" {
			n.mu.Unlock()
			CopyTaskManager.Start()
			return true, nil
		}
		if n.record.RestartRequired && !restart {
			n.mu.Unlock()
			return false, errors.New("下载源无法安全续传，需单独确认从头重新下载")
		}
		prior := n.record
		n.record.State = "waiting"
		n.record.Restart = restart
		n.record.Error = ""
		n.record.EndTime = nil
		n.mu.Unlock()
		if err := SaveNASDownloads(); err != nil {
			n.mu.Lock()
			n.record = prior
			n.mu.Unlock()
			return false, err
		}
		enqueueNAS(n, user)
		return true, nil
	}
	if action == "pause" && state == "waiting" && len(pauseWaiting) > 0 && pauseWaiting[0] {
		n.record.State = "paused"
		n.mu.Unlock()
		return true, SaveNASDownloads()
	}
	if action == "pause" && state != "downloading" {
		n.mu.Unlock()
		return false, nil
	}
	if action == "cancel" && (nasTerminal(state) || state == "canceling") {
		n.mu.Unlock()
		return false, nil
	}
	if action != "pause" && action != "cancel" {
		n.mu.Unlock()
		return false, errors.New("无效任务操作")
	}
	if active {
		if action == "pause" {
			n.record.State = "pausing"
		} else {
			n.record.State = "canceling"
		}
	} else {
		n.record.State = "canceled"
		n.record.CompletionReason = "user_canceled"
		now := time.Now()
		n.record.EndTime = &now
	}
	cancel, done := n.cancel, n.done
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if active && done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if action == "cancel" && !active {
		if err := removeNASPartial(n); err != nil {
			n.mu.Lock()
			n.record.Error = "传输已取消，但临时文件清理失败，请检查 NAS 挂载和权限"
			n.mu.Unlock()
			_ = SaveNASDownloads()
			return false, err
		}
	}
	return true, SaveNASDownloads()
}

// Pause waiting records before canceling running transfers. Workers acquire the
// same registry lock before entering a run, so freed slots cannot start them.
func PauseNASWaiting() (map[string]bool, error) {
	if err := NASDownloadsAvailable(); err != nil {
		return nil, err
	}
	paused := map[string]bool{}
	nasRegistry.Lock()
	for id, n := range nasRegistry.tasks {
		n.mu.Lock()
		if n.record.State == "waiting" {
			n.record.State = "paused"
			paused[id] = true
		}
		n.mu.Unlock()
	}
	nasRegistry.Unlock()
	return paused, SaveNASDownloads()
}
func (t *CopyTask) runNAS() (err error) {
	n := t.NAS
	nasRegistry.Lock()
	n.mu.Lock()
	nasRegistry.Unlock()
	n.queued = false
	if n.active || n.record.State != "waiting" {
		n.mu.Unlock()
		return context.Canceled
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.TaskExtension.Base.Ctx(), "user", t.GetCreator()))
	n.active = true
	n.cancel = cancel
	n.done = make(chan struct{})
	n.record.State = "downloading"
	now := time.Now()
	n.record.StartTime = &now
	n.record.EndTime = nil
	n.mu.Unlock()
	n.meter.Start(now)
	persistErr := SaveNASDownloads()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.New("下载执行异常，临时文件已保留，可重试")
		}
		cancel()
		n.meter.Stop(err)
		n.mu.Lock()
		intent := n.record.State
		switch {
		case intent == "completed":
			n.record.CompletionReason = "completed"
		case intent == "pausing":
			n.record.State = "paused"
			n.record.CompletionReason = ""
		case intent == "canceling":
			n.record.State = "canceled"
			n.record.CompletionReason = "user_canceled"
		case errors.Is(err, errNASRestart):
			n.record.State = "paused"
			n.record.RestartRequired = true
			n.record.Error = err.Error()
		case err != nil:
			n.record.State = "failed"
			n.record.Error = safeNASError(err)
			n.record.CompletionReason = task.CompletionReason(err)
		default:
			n.record.State = "completed"
			n.record.Error = ""
			n.record.CompletionReason = "completed"
		}
		if n.record.State != "paused" {
			ended := time.Now()
			n.record.EndTime = &ended
		}
		n.active = false
		n.cancel = nil
		done := n.done
		n.mu.Unlock()
		if intent == "canceling" {
			if cleanupErr := removeNASPartial(n); cleanupErr != nil {
				n.mu.Lock()
				n.record.Error = "传输已取消，但临时文件清理失败，请检查 NAS 挂载和权限"
				n.mu.Unlock()
			}
		}
		_ = SaveNASDownloads()
		close(done)
	}()
	if persistErr != nil {
		return fmt.Errorf("保存任务状态失败，未开始传输：%w", persistErr)
	}
	return transferNAS(ctx, n)
}
func safeNASError(err error) string {
	// Transport errors may include signed URLs. Never put them into task history.
	if errors.Is(err, context.Canceled) {
		return "传输已取消"
	}
	if task.CompletionReason(err) == "timeout" {
		return "下载超时，已保留临时文件，可开始任务继续"
	}
	return err.Error()
}

// Keep legacy copy tasks unchanged; synchronized overrides only apply to the
// NAS executions, which are excluded from the legacy JSON task journal.
func (t *CopyTask) Persistable() bool { return t.NAS == nil }
func (t *CopyTask) Retryable() bool   { return t.NAS == nil }
func (t *CopyTask) SetState(state tache.State) {
	if t.NAS == nil {
		t.TaskExtension.SetState(state)
		return
	}
	t.executionMu.Lock()
	t.TaskExtension.Base.State = state
	t.executionMu.Unlock()
}
func (t *CopyTask) GetState() tache.State {
	if t.NAS == nil {
		return t.TaskExtension.GetState()
	}
	t.executionMu.Lock()
	defer t.executionMu.Unlock()
	return t.TaskExtension.Base.State
}
func (t *CopyTask) SetErr(err error) {
	if t.NAS == nil {
		t.TaskExtension.SetErr(err)
		return
	}
	t.executionMu.Lock()
	t.executionErr = err
	t.executionMu.Unlock()
}
func (t *CopyTask) GetErr() error {
	if t.NAS == nil {
		return t.TaskExtension.GetErr()
	}
	t.executionMu.Lock()
	defer t.executionMu.Unlock()
	return t.executionErr
}
func (t *CopyTask) GetRetry() (int, int) {
	if t.NAS == nil {
		return t.TaskExtension.GetRetry()
	}
	return 0, 0
}
func (t *CopyTask) SetRetry(i, j int) {
	if t.NAS == nil {
		t.TaskExtension.SetRetry(i, j)
	}
}
func (t *CopyTask) NASView() (NASView, bool) {
	if t.NAS == nil {
		return NASView{}, false
	}
	return t.NAS.view(), true
}
func checkpointNASDownloads() {
	nasCheckpointMu.Lock()
	if time.Since(nasCheckpointAt) < 2*time.Second {
		nasCheckpointMu.Unlock()
		return
	}
	nasCheckpointAt = time.Now()
	nasCheckpointMu.Unlock()
	_ = SaveNASDownloads()
}
