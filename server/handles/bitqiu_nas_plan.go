package handles

import (
	"crypto/rand"
	"errors"
	"os"
	"path"
	"path/filepath"
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
)

type nasConflict struct {
	ID               string    `json:"id"`
	Path             string    `json:"path"`
	ExistingSize     int64     `json:"existing_size"`
	ExistingModified time.Time `json:"existing_modified"`
	IncomingSize     int64     `json:"incoming_size"`
	IncomingModified time.Time `json:"incoming_modified"`
	CanReplace       bool      `json:"can_replace"`
}
type nasPlan struct {
	mu        sync.Mutex
	owner     uint
	expires   time.Time
	records   []fs.NASRecord
	conflicts []nasConflict
	used      bool
}

var nasPlans = struct {
	sync.Mutex
	items map[string]*nasPlan
}{items: map[string]*nasPlan{}}

func buildNASRecords(c *gin.Context, req MoveCopyReq, destination string) ([]fs.NASRecord, error) {
	user := c.MustGet("user").(*model.User)
	source, actual, virtual, err := nasSelectionSource(user, req.SrcDir)
	if err != nil {
		return nil, err
	}
	entries, _, err := walkNASSelection(c.Request.Context(), source, actual, req.Names)
	if err != nil {
		return nil, err
	}
	target, actualTarget, err := op.GetStorageAndActualPath(destination)
	if err != nil {
		return nil, err
	}
	localDriver, _, err := localNASDirectory(destination)
	if err != nil {
		return nil, err
	}
	provider, ok := localDriver.GetAddition().(driver.IRootPath)
	if !ok {
		return nil, errors.New("NAS 本地存储缺少根目录")
	}
	groups := map[string]string{}
	for _, name := range req.Names {
		obj, err := op.Get(c.Request.Context(), source, path.Join(actual, name))
		if err != nil {
			return nil, err
		}
		if obj.IsDir() {
			groups[name] = "group-" + rand.Text()
		}
	}
	batch := rand.Text()
	records := make([]fs.NASRecord, 0, len(entries))
	for _, entry := range entries {
		relative := strings.TrimPrefix(path.Join(actualTarget, entry.relative), "/")
		if !filepath.IsLocal(filepath.FromSlash(relative)) {
			return nil, errors.New("无效目标文件路径")
		}
		record, err := fs.NewNASRecord(user, path.Join(virtual, entry.relative), path.Join(destination, entry.relative), provider.GetRootPath(), relative, source.GetStorage().MountPath, target.GetStorage().MountPath, entry.source, entry.obj)
		if err != nil {
			return nil, err
		}
		top := strings.SplitN(entry.relative, "/", 2)[0]
		record.BatchID = batch
		record.GroupID = groups[top]
		if record.GroupID != "" {
			record.GroupName = top
		}
		records = append(records, record)
	}
	return records, nil
}
func BitQiuNASPrepare(c *gin.Context) {
	var req MoveCopyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Names) == 0 || len(req.Names) > 50000 {
		common.ErrorStrResp(c, "请选择 1 至 50000 项", 400)
		return
	}
	seen := map[string]bool{}
	for _, name := range req.Names {
		if err := utils.ValidateNameComponent(name); err != nil || seen[name] {
			common.ErrorStrResp(c, "无效或重复的文件名称", 400)
			return
		}
		seen[name] = true
	}
	user := c.MustGet("user").(*model.User)
	destination, err := user.JoinPath(req.DstDir)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	records, err := buildNASRecords(c, req, destination)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	conflicts := []nasConflict{}
	for i := range records {
		r := &records[i]
		root, err := os.OpenRoot(r.Root)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		err = fs.NASCheckParents(root, r.Relative)
		if err != nil {
			root.Close()
			common.ErrorResp(c, err, 409)
			return
		}
		version, info, err := fs.NASPathVersion(root, r.Relative)
		root.Close()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		if r.IsDir && info.IsDir() {
			continue
		}
		r.TargetVersion = version
		conflicts = append(conflicts, nasConflict{ID: r.ID, Path: r.Destination, ExistingSize: info.Size(), ExistingModified: info.ModTime(), IncomingSize: r.TotalBytes, IncomingModified: r.Modified, CanReplace: !r.IsDir && info.Mode().IsRegular()})
	}
	// Refuse an occupied destination before asking about overwrite. A duplicate
	// live/paused/failed transfer is controlled through its original task.
	existing := fs.NASDownloads()
	for _, r := range records {
		for _, v := range existing {
			if v.Root == r.Root && v.Relative == r.Relative && v.State != "completed" && v.State != "canceled" {
				common.ErrorStrResp(c, "该目标已有任务，请操作原任务或选择其他目录："+r.Name, 409)
				return
			}
		}
	}
	plan := &nasPlan{owner: user.ID, expires: time.Now().Add(15 * time.Minute), records: records, conflicts: conflicts}
	id := rand.Text()
	nasPlans.Lock()
	for key, p := range nasPlans.items {
		if time.Now().After(p.expires) {
			delete(nasPlans.items, key)
		}
	}
	cached := 0
	for _, p := range nasPlans.items {
		cached += len(p.records)
	}
	if len(nasPlans.items) >= 16 || cached+len(records) > 200000 {
		nasPlans.Unlock()
		common.ErrorStrResp(c, "下载预检查繁忙，请稍后重试", 429)
		return
	}
	nasPlans.items[id] = plan
	nasPlans.Unlock()
	common.SuccessResp(c, gin.H{"plan_id": id, "conflicts": conflicts, "expires_at": plan.expires})
}
func BitQiuNASCommit(c *gin.Context) {
	var req struct {
		PlanID  string            `json:"plan_id"`
		Choices map[string]string `json:"choices"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	nasPlans.Lock()
	plan := nasPlans.items[req.PlanID]
	nasPlans.Unlock()
	user := c.MustGet("user").(*model.User)
	if plan == nil || plan.owner != user.ID {
		common.ErrorStrResp(c, "预检查已失效，请重新提交下载", 409)
		return
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.used || time.Now().After(plan.expires) {
		common.ErrorStrResp(c, "预检查已使用或过期，请重新提交下载", 409)
		return
	}
	conflicts := map[string]nasConflict{}
	for _, conflict := range plan.conflicts {
		conflicts[conflict.ID] = conflict
		choice := req.Choices[conflict.ID]
		if choice != "skip" && (choice != "replace" || !conflict.CanReplace) {
			common.ErrorStrResp(c, "请为每个冲突明确选择替换或跳过", 400)
			return
		}
	}
	records := append([]fs.NASRecord(nil), plan.records...)
	skipped := 0
	for i := range records {
		r := &records[i]
		if _, ok := conflicts[r.ID]; ok {
			if req.Choices[r.ID] == "skip" {
				now := time.Now()
				r.State = "completed"
				r.CompletionReason = "skipped"
				r.EndTime = &now
				skipped++
				continue
			}
			r.Replace = true
		}
		if err := fs.NASValidateTarget(*r); err != nil {
			common.ErrorResp(c, err, 409)
			return
		}
	}
	if err := fs.QueueNASDownloads(records, user); err != nil {
		common.ErrorResp(c, err, 409)
		return
	}
	plan.used = true
	// Keep the small used marker to reject double submission, release record data.
	nasPlans.Lock()
	delete(nasPlans.items, req.PlanID)
	nasPlans.Unlock()
	tasks := []gin.H{}
	for _, r := range records {
		tasks = append(tasks, gin.H{"id": r.ID, "name": r.Name})
	}
	common.SuccessResp(c, gin.H{"tasks": tasks, "skipped": skipped})
}
func BitQiuNASClearHistory(c *gin.Context) {
	var req struct {
		Scope          string   `json:"scope"`
		IDs            []string `json:"ids"`
		Confirmed      bool     `json:"confirmed"`
		RemovePartials bool     `json:"remove_partials"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if !req.Confirmed || (req.Scope != "all" && req.Scope != "selected") || len(req.IDs) > 50000 || (req.Scope == "selected" && len(req.IDs) == 0) {
		common.ErrorStrResp(c, "请确认清理范围（仅删除历史记录）", 400)
		return
	}
	result, err := fs.ClearNASHistory(req.IDs, req.Scope == "all", req.RemovePartials, time.Time{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, result)
}
