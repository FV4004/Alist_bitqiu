package handles

import (
	"context"
	"github.com/alist-org/alist/v3/internal/model"
	taskpkg "github.com/alist-org/alist/v3/internal/task"
	"math"
	"time"

	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/offline_download/tool"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	"github.com/xhofe/tache"
)

type TaskInfo struct {
	ID                  string      `json:"id"`
	Name                string      `json:"name"`
	Creator             string      `json:"creator"`
	CreatorRole         model.Roles `json:"creator_role"`
	State               tache.State `json:"state"`
	Status              string      `json:"status"`
	Progress            float64     `json:"progress"`
	StartTime           *time.Time  `json:"start_time"`
	EndTime             *time.Time  `json:"end_time"`
	TotalBytes          int64       `json:"total_bytes"`
	Error               string      `json:"error"`
	TransferredBytes    int64       `json:"transferred_bytes"`
	SpeedBytesPerSecond float64     `json:"speed_bytes_per_second"`
	CompletionReason    string      `json:"completion_reason,omitempty"`
}

func getTaskInfo[T taskpkg.TaskExtensionInfo](task T) TaskInfo {
	if controlled, ok := any(task).(interface{ NASView() (fs.NASView, bool) }); ok {
		if v, ok := controlled.NASView(); ok {
			var state tache.State = tache.StatePending
			switch v.State {
			case "downloading":
				state = tache.StateRunning
			case "pausing", "canceling":
				state = tache.StateCanceling
			case "paused", "canceled":
				state = tache.StateCanceled
			case "completed":
				state = tache.StateSucceeded
			case "failed":
				state = tache.StateFailed
			}
			return TaskInfo{ID: v.ID, Name: v.Name, State: state, Status: v.State, Progress: v.Progress, StartTime: v.StartTime, EndTime: v.EndTime, TotalBytes: v.TotalBytes, TransferredBytes: v.TransferredBytes, SpeedBytesPerSecond: v.Speed, CompletionReason: v.CompletionReason, Error: v.Error}
		}
	}

	errMsg := ""
	if task.GetErr() != nil {
		errMsg = task.GetErr().Error()
	}
	progress := task.GetProgress()
	// if progress is NaN, set it to 100
	if math.IsNaN(progress) {
		progress = 100
	}
	creatorName := ""
	var creatorRole model.Roles
	if task.GetCreator() != nil {
		creatorName = task.GetCreator().Username
		creatorRole = task.GetCreator().Role
	}
	var transfer taskpkg.TransferSnapshot
	if measured, ok := any(task).(interface {
		GetTransferSnapshot() taskpkg.TransferSnapshot
	}); ok {
		transfer = measured.GetTransferSnapshot()
	}
	reason := ""
	switch task.GetState() {
	case tache.StateSucceeded:
		reason = "completed"
	case tache.StateCanceled:
		reason = "canceled"
	case tache.StateFailed:
		reason = taskpkg.CompletionReason(task.GetErr())
		if reason == "completed" {
			reason = "failed"
		}
	}
	if reason != "" && transfer.CompletionReason != "" && transfer.CompletionReason != "completed" {
		reason = transfer.CompletionReason
	}
	speed := float64(0)
	if task.GetState() == tache.StateRunning {
		speed = transfer.SpeedBytesPerSecond
	}
	return TaskInfo{
		ID:                  task.GetID(),
		Name:                task.GetName(),
		Creator:             creatorName,
		CreatorRole:         creatorRole,
		State:               task.GetState(),
		Status:              task.GetStatus(),
		Progress:            progress,
		StartTime:           task.GetStartTime(),
		EndTime:             task.GetEndTime(),
		TotalBytes:          task.GetTotalBytes(),
		Error:               errMsg,
		TransferredBytes:    transfer.TransferredBytes,
		SpeedBytesPerSecond: speed,
		CompletionReason:    reason,
	}
}

func getTaskInfos[T taskpkg.TaskExtensionInfo](tasks []T) []TaskInfo {
	return utils.MustSliceConvert(tasks, getTaskInfo[T])
}

func argsContains[T comparable](v T, slice ...T) bool {
	return utils.SliceContains(slice, v)
}

func getUserInfo(c *gin.Context) (bool, uint, bool) {
	if user, ok := c.Value("user").(*model.User); ok {
		return user.IsAdmin(), user.ID, true
	} else {
		return false, 0, false
	}
}

func getTargetedHandler[T taskpkg.TaskExtensionInfo](manager taskpkg.Manager[T], callback func(c *gin.Context, task T)) gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		t, ok := manager.GetByID(c.Query("tid"))
		if !ok {
			common.ErrorStrResp(c, "task not found", 404)
			return
		}
		if !isAdmin && uid != t.GetCreator().ID {
			// to avoid an attacker using error messages to guess valid TID, return a 404 rather than a 403
			common.ErrorStrResp(c, "task not found", 404)
			return
		}
		callback(c, t)
	}
}

func getBatchHandler[T taskpkg.TaskExtensionInfo](manager taskpkg.Manager[T], callback func(task T)) gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		var tids []string
		if err := c.ShouldBind(&tids); err != nil {
			common.ErrorStrResp(c, "invalid request format", 400)
			return
		}
		retErrs := make(map[string]string)
		for _, tid := range tids {
			t, ok := manager.GetByID(tid)
			if !ok || (!isAdmin && uid != t.GetCreator().ID) {
				retErrs[tid] = "task not found"
				continue
			}
			callback(t)
		}
		common.SuccessResp(c, retErrs)
	}
}

func retryTask[T taskpkg.TaskExtensionInfo](manager taskpkg.Manager[T], t T) error {
	if controlled, ok := any(t).(interface{ NASView() (fs.NASView, bool) }); ok {
		if v, yes := controlled.NASView(); yes {
			_, err := fs.ControlNASDownload(context.Background(), v.ID, "start", false, t.GetCreator())
			return err
		}
	}
	manager.Retry(t.GetID())
	return nil
}

func taskRoute[T taskpkg.TaskExtensionInfo](g *gin.RouterGroup, manager taskpkg.Manager[T]) {
	g.GET("/undone", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		common.SuccessResp(c, getTaskInfos(manager.GetByCondition(func(task T) bool {
			// avoid directly passing the user object into the function to reduce closure size
			return (isAdmin || uid == task.GetCreator().ID) &&
				argsContains(task.GetState(), tache.StatePending, tache.StateRunning, tache.StateCanceling,
					tache.StateErrored, tache.StateFailing, tache.StateWaitingRetry, tache.StateBeforeRetry)
		})))
	})
	g.GET("/done", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		common.SuccessResp(c, getTaskInfos(manager.GetByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) &&
				argsContains(task.GetState(), tache.StateCanceled, tache.StateFailed, tache.StateSucceeded)
		})))
	})
	g.POST("/info", getTargetedHandler(manager, func(c *gin.Context, task T) {
		common.SuccessResp(c, getTaskInfo(task))
	}))
	g.POST("/cancel", getTargetedHandler(manager, func(c *gin.Context, task T) {
		manager.Cancel(task.GetID())
		common.SuccessResp(c)
	}))
	g.POST("/delete", getTargetedHandler(manager, func(c *gin.Context, task T) {
		manager.Remove(task.GetID())
		common.SuccessResp(c)
	}))
	g.POST("/retry", getTargetedHandler(manager, func(c *gin.Context, task T) {
		if err := retryTask(manager, task); err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		common.SuccessResp(c)
	}))
	g.POST("/cancel_some", getBatchHandler(manager, func(task T) {
		manager.Cancel(task.GetID())
	}))
	g.POST("/delete_some", getBatchHandler(manager, func(task T) {
		manager.Remove(task.GetID())
	}))
	g.POST("/retry_some", getBatchHandler(manager, func(task T) {
		_ = retryTask(manager, task)
	}))
	g.POST("/clear_done", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		manager.RemoveByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) &&
				argsContains(task.GetState(), tache.StateCanceled, tache.StateFailed, tache.StateSucceeded)
		})
		common.SuccessResp(c)
	})
	g.POST("/clear_succeeded", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		manager.RemoveByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) && task.GetState() == tache.StateSucceeded
		})
		common.SuccessResp(c)
	})
	g.POST("/retry_failed", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		tasks := manager.GetByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) && task.GetState() == tache.StateFailed
		})
		for _, t := range tasks {
			_ = retryTask(manager, t)
		}
		common.SuccessResp(c)
	})
}

func SetupTaskRoute(g *gin.RouterGroup) {
	taskRoute(g.Group("/upload"), fs.UploadTaskManager)
	taskRoute(g.Group("/copy"), fs.CopyTaskManager)
	taskRoute(g.Group("/offline_download"), tool.DownloadTaskManager)
	taskRoute(g.Group("/offline_download_transfer"), tool.TransferTaskManager)
	taskRoute(g.Group("/s3_transition"), fs.S3TransitionTaskManager)
	taskRoute(g.Group("/decompress"), fs.ArchiveDownloadTaskManager)
	taskRoute(g.Group("/decompress_upload"), fs.ArchiveContentUploadTaskManager)
}
