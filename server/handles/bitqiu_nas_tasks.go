package handles

import (
	"context"
	"errors"
	"math"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

type nasSelection struct {
	SrcDir string   `json:"src_dir"`
	Names  []string `json:"names"`
}
type nasEntry struct {
	source   string
	relative string
	obj      model.Obj
}
type nasSelectionStats struct {
	SelectedCount int   `json:"selected_count"`
	FileCount     int   `json:"file_count"`
	KnownBytes    int64 `json:"known_bytes"`
	UnknownCount  int   `json:"unknown_count"`
}

func walkNASSelection(ctx context.Context, source driver.Driver, actual string, names []string) ([]nasEntry, nasSelectionStats, error) {
	stats := nasSelectionStats{SelectedCount: len(names)}
	entries := []nasEntry{}
	seen := map[string]bool{}
	var walk func(string, string, int) error
	walk = func(src, relative string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 128 || len(seen) >= 200000 {
			return errors.New("目录统计超过安全上限，请分批选择")
		}
		obj, err := op.Get(ctx, source, src)
		if err != nil {
			return err
		}
		key := obj.GetID()
		if key == "" {
			key = obj.GetPath()
		}
		if key == "" {
			key = src
		}
		if obj.IsDir() {
			key = "dir:" + key
		} else {
			key = "file:" + key
		}
		if seen[key] {
			return nil
		}
		seen[key] = true
		if !obj.IsDir() {
			stats.FileCount++
			if model.ObjectSizeKnown(obj) && obj.GetSize() <= math.MaxInt64-stats.KnownBytes {
				stats.KnownBytes += obj.GetSize()
			} else {
				stats.UnknownCount++
			}
			entries = append(entries, nasEntry{src, relative, obj})
			return nil
		}
		children, err := op.List(ctx, source, src, model.ListArgs{})
		if err != nil {
			return err
		}
		if len(children) == 0 {
			entries = append(entries, nasEntry{src, relative, obj})
		}
		for _, child := range children {
			if err := utils.ValidateNameComponent(child.GetName()); err != nil {
				return err
			}
			if err = walk(path.Join(src, child.GetName()), path.Join(relative, child.GetName()), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	top := map[string]bool{}
	for _, name := range names {
		if err := utils.ValidateNameComponent(name); err != nil {
			return nil, stats, err
		}
		if top[name] {
			continue
		}
		top[name] = true
		if err := walk(path.Join(actual, name), name, 0); err != nil {
			return nil, stats, err
		}
	}
	stats.SelectedCount = len(top)
	return entries, stats, nil
}
func nasSelectionSource(user *model.User, dir string) (driver.Driver, string, string, error) {
	virtual, err := user.JoinPath(dir)
	if err != nil {
		return nil, "", "", err
	}
	if !common.CanReadPathByRole(user, virtual) {
		return nil, "", "", errors.New("无权读取所选目录")
	}
	d, actual, err := op.GetStorageAndActualPath(virtual)
	if err != nil {
		return nil, "", "", err
	}
	if d.GetStorage().Driver != "BitQiu" {
		return nil, "", "", errors.New("请选择 BitQiu 存储")
	}
	return d, actual, virtual, nil
}
func BitQiuNASStats(c *gin.Context) {
	var req nasSelection
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Names) == 0 || len(req.Names) > 50000 {
		common.ErrorStrResp(c, "请选择 1 至 50000 项", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	d, actual, _, err := nasSelectionSource(user, req.SrcDir)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, stats, err := walkNASSelection(c.Request.Context(), d, actual, req.Names)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, stats)
}
func queueNASSelection(c *gin.Context, req MoveCopyReq, destination string) error {
	user := c.MustGet("user").(*model.User)
	source, actual, virtual, err := nasSelectionSource(user, req.SrcDir)
	if err != nil {
		return err
	}
	entries, _, err := walkNASSelection(c.Request.Context(), source, actual, req.Names)
	if err != nil {
		return err
	}
	target, actualTarget, err := op.GetStorageAndActualPath(destination)
	if err != nil {
		return err
	}
	localDriver, _, err := localNASDirectory(destination)
	if err != nil {
		return err
	}
	rootProvider, ok := localDriver.GetAddition().(driver.IRootPath)
	if !ok {
		return errors.New("NAS 本地存储缺少根目录")
	}
	root := rootProvider.GetRootPath()
	records := make([]fs.NASRecord, 0, len(entries))
	for _, entry := range entries {
		relative := strings.TrimPrefix(path.Join(actualTarget, entry.relative), "/")
		if !filepath.IsLocal(filepath.FromSlash(relative)) {
			return errors.New("无效目标文件路径")
		}
		record, err := fs.NewNASRecord(user, path.Join(virtual, entry.relative), path.Join(destination, entry.relative), root, relative, source.GetStorage().MountPath, target.GetStorage().MountPath, entry.source, entry.obj)
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	if err = fs.QueueNASDownloads(records, user); err != nil {
		return err
	}
	ids := make([]gin.H, 0, len(records))
	for _, record := range records {
		ids = append(ids, gin.H{"id": record.ID, "name": record.Name})
	}
	common.SuccessResp(c, gin.H{"tasks": ids})
	return nil
}
func BitQiuNASTasks(c *gin.Context) {
	if err := fs.NASDownloadsAvailable(); err != nil {
		common.ErrorResp(c, err, 503)
		return
	}
	active, history := []fs.NASView{}, []fs.NASView{}
	for _, v := range fs.NASDownloads() {
		// Internal paths and validators belong only in the durable journal.
		v.Root = ""
		v.Relative = ""
		v.SrcPath = ""
		v.Validator = ""
		v.ValidatorKind = ""
		if v.State == "completed" || v.State == "canceled" || v.State == "failed" {
			history = append(history, v)
		} else {
			active = append(active, v)
		}
	}
	common.SuccessResp(c, gin.H{"active": active, "history": history, "concurrency": fs.NASConcurrencyStatus()})
}
func BitQiuNASControl(c *gin.Context) {
	var req struct {
		Scope     string   `json:"scope"`
		IDs       []string `json:"ids"`
		Action    string   `json:"action"`
		Confirmed bool     `json:"confirmed"`
		Restart   bool     `json:"restart"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Scope != "all" && req.Scope != "selected" || req.Action != "start" && req.Action != "pause" && req.Action != "cancel" {
		common.ErrorStrResp(c, "无效的任务操作范围或动作", 400)
		return
	}
	if req.Action == "cancel" && !req.Confirmed {
		common.ErrorStrResp(c, "取消操作需要二次确认", 400)
		return
	}
	if req.Scope == "selected" && (len(req.IDs) == 0 || len(req.IDs) > 50000) {
		common.ErrorStrResp(c, "请选择需要操作的任务", 400)
		return
	}
	if req.Restart && (req.Scope != "selected" || len(req.IDs) != 1) {
		common.ErrorStrResp(c, "从头重新下载必须单独确认任务", 400)
		return
	}
	if req.Scope == "all" {
		req.IDs = nil
		for _, v := range fs.NASDownloads() {
			req.IDs = append(req.IDs, v.ID)
		}
	}
	user := c.MustGet("user").(*model.User)
	paused := map[string]bool{}
	if req.Scope == "all" && req.Action == "pause" {
		var err error
		paused, err = fs.PauseNASWaiting()
		if err != nil {
			common.ErrorResp(c, err, 503)
			return
		}
	}
	success, failed, skipped := 0, 0, 0
	details := []gin.H{}
	seen := map[string]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if paused[id] {
			success++
			continue
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		applied, err := fs.ControlNASDownload(ctx, id, req.Action, req.Restart, user, req.Scope == "all")
		cancel()
		if err != nil {
			failed++
			details = append(details, gin.H{"id": id, "error": err.Error()})
		} else if applied {
			success++
		} else {
			skipped++
		}
	}
	common.SuccessResp(c, gin.H{"success": success, "failed": failed, "skipped": skipped, "details": details})
}

func BitQiuNASConcurrency(c *gin.Context) {
	var req struct {
		Limit int `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := fs.SetNASConcurrency(ctx, req.Limit)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, result)
}
