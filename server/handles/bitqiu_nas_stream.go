package handles

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"time"

	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	pkgerrors "github.com/pkg/errors"
)

type nasListedItem struct {
	Name       string    `json:"name"`
	Path       string    `json:"path,omitempty"`
	Label      string    `json:"label,omitempty"`
	IsDir      bool      `json:"is_dir"`
	Selectable bool      `json:"selectable"`
	Size       int64     `json:"size"`
	SizeKnown  bool      `json:"size_known"`
	Modified   time.Time `json:"modified"`
}

func BitQiuNASEntries(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	virtual, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if !common.CanReadPathByRole(user, virtual) {
		common.ErrorStrResp(c, "无权浏览所选 NAS 目录", 403)
		return
	}
	_, full, err := localNASDirectory(virtual)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// ReadDir does not follow child symlinks. Files, links and special objects
	// remain visible but cannot be used as a destination folder.
	entries, err := os.ReadDir(full)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	result := []nasListedItem{}
	for _, entry := range entries {
		if !common.CanReadPathByRole(user, path.Join(virtual, entry.Name())) {
			continue
		}
		item := nasListedItem{Name: entry.Name(), Label: entry.Name(), Path: entry.Name(), IsDir: entry.IsDir(), Selectable: entry.IsDir()}
		if info, err := entry.Info(); err == nil {
			item.Size = info.Size()
			item.SizeKnown = info.Mode().IsRegular()
			item.Modified = info.ModTime()
			if info.Mode()&os.ModeSymlink != 0 {
				item.Selectable = false
				item.IsDir = false
			}
		}
		result = append(result, item)
	}
	common.SuccessResp(c, result)
}

// Frames are provisional until "done". The ordinary List return and cache stay
// all-or-nothing: neither a failed page nor a disconnected consumer caches a
// partial directory. No pagination cursor or account state leaves the server.
func BitQiuNASListStream(c *gin.Context) {
	var req struct {
		Path    string `json:"path"`
		Refresh bool   `json:"refresh"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	storage, actual, virtual, err := nasSelectionSource(user, req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(virtual)
	if err != nil && !errors.Is(pkgerrors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500)
		return
	}
	if !common.CanAccessWithRoles(user, meta, virtual, "") {
		common.ErrorStrResp(c, "无权浏览所选网盘目录", 403)
		return
	}
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	write := func(frame any) error {
		if err := c.Request.Context().Err(); err != nil {
			return err
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		if _, err = c.Writer.Write(raw); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	}
	if err = write(gin.H{"event": "start"}); err != nil {
		return
	}
	sent := map[string]bool{}
	visibleNames := model.NewObjMerge()
	if meta != nil && meta.Hide != "" && !common.HasPermission(common.MergeRolePermissions(user, virtual), common.PermSeeHides) && (utils.PathEqual(meta.Path, virtual) || meta.HSub) {
		visibleNames.InitHideReg(meta.Hide)
	}
	emit := func(objects []model.Obj) error {
		if err := c.Request.Context().Err(); err != nil {
			return err
		}
		// Wrapping a copied slice applies the same names as the complete List path
		// without mutating objects concurrently used by another request's cache.
		visible := append([]model.Obj(nil), objects...)
		for i, obj := range visible {
			if _, wrapped := obj.(*model.ObjWrapName); !wrapped {
				visible[i] = model.WrapObjName(obj)
			}
		}
		visible = visibleNames.Merge(visible)
		batch := []nasListedItem{}
		for _, obj := range visible {
			if !common.CanReadPathByRole(user, path.Join(virtual, obj.GetName())) || sent[obj.GetName()] {
				continue
			}
			if len(sent) >= 200000 {
				return errors.New("目录超过 200000 项，请拆分目录后重试")
			}
			sent[obj.GetName()] = true
			batch = append(batch, nasListedItem{Name: obj.GetName(), IsDir: obj.IsDir(), Selectable: true, Size: obj.GetSize(), SizeKnown: model.ObjectSizeKnown(obj), Modified: obj.ModTime()})
			if len(batch) == 128 {
				if err := write(gin.H{"event": "batch", "content": batch}); err != nil {
					return err
				}
				batch = []nasListedItem{}
			}
		}
		if len(batch) > 0 {
			return write(gin.H{"event": "batch", "content": batch})
		}
		return nil
	}
	files, err := op.List(c.Request.Context(), storage, actual, model.ListArgs{Refresh: req.Refresh, ReqPath: virtual, OnPage: emit})
	if err == nil {
		err = emit(files)
		if err == nil {
			err = emit(op.GetStorageVirtualFilesByPath(virtual))
		}
	} // cache hits and non-streaming fixture drivers
	if err != nil {
		if c.Request.Context().Err() == nil && !errors.Is(err, io.ErrClosedPipe) {
			_ = write(gin.H{"event": "error", "message": "目录读取未完成，请刷新重试：" + err.Error()})
		}
		return
	}
	_ = write(gin.H{"event": "done", "total": len(sent)})
}
