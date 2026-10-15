package handles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/alist-org/alist/v3/drivers/local"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

const nasDownloadRoot = "/downloads"
const nasDownloadMount = "/NAS下载"

var nasEnableMu sync.Mutex

type nasTarget struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// All handlers are registered under Auth + AuthAdmin. Never return a storage
// Addition: it can contain the BitQiu password or saved access tokens.
func BitQiuNASTargets(c *gin.Context) {
	cloud, targets := []nasTarget{}, []nasTarget{}
	downloadsEnabled := false
	for _, d := range op.GetAllStorages() {
		st := d.GetStorage()
		if st.Disabled {
			continue
		}
		item := nasTarget{Path: st.MountPath, Label: st.MountPath}
		switch st.Driver {
		case "BitQiu":
			cloud = append(cloud, item)
		case "Local":
			if l, ok := d.(*local.Local); ok && filepath.Clean(l.GetRootPath()) == nasDownloadRoot {
				downloadsEnabled = true
			}
			targets = append(targets, item)
		}
	}
	sort.Slice(cloud, func(i, j int) bool { return cloud[i].Path < cloud[j].Path })
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	info, err := os.Stat(nasDownloadRoot)
	common.SuccessResp(c, gin.H{"cloud": cloud, "targets": targets, "downloads_ready": err == nil && info.IsDir(), "downloads_enabled": downloadsEnabled})
}
func enableNASDownloads(ctx context.Context, root, mount string) error {
	nasEnableMu.Lock()
	defer nasEnableMu.Unlock()
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errors.New("请先在极空间 Docker 界面把 NAS 文件夹挂载到容器 /downloads，再重新创建容器")
	}
	if current, err := op.GetStorageByMountPath(mount); err == nil {
		l, ok := current.(*local.Local)
		if ok && filepath.Clean(l.GetRootPath()) == filepath.Clean(root) && !l.Disabled {
			return nil
		}
		return errors.New("NAS 下载挂载名已被其他存储占用，请先在存储管理中处理")
	}
	// Local storages otherwise inherit the site's guest read access. Protect
	// this new mount before exposing it; administrators bypass metadata passwords.
	meta, err := op.GetMetaByPath(mount)
	if err != nil && !errors.Is(err, errs.MetaNotFound) {
		return err
	}
	protected := model.Meta{Path: mount}
	if meta != nil {
		protected = *meta
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	protected.Password, protected.PSub = hex.EncodeToString(secret), true
	if protected.ID == 0 {
		err = op.CreateMeta(&protected)
	} else {
		err = op.UpdateMeta(&protected)
	}
	if err != nil {
		return err
	}
	addition, _ := json.Marshal(map[string]any{"root_folder_path": root, "show_hidden": true, "mkdir_perm": "755"})
	_, err = op.CreateStorage(ctx, model.Storage{Driver: "Local", MountPath: mount, Addition: string(addition), Remark: "BitQiu NAS 下载目录", DisableIndex: true})
	return err
}
func BitQiuNASEnable(c *gin.Context) {
	if err := enableNASDownloads(c, nasDownloadRoot, nasDownloadMount); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c)
}

func localNASDirectory(virtualPath string) (driver.Driver, string, error) {
	d, actual, err := op.GetStorageAndActualPath(virtualPath)
	if err != nil {
		return nil, "", err
	}
	l, ok := d.(*local.Local)
	if !ok || l.Disabled {
		return nil, "", errors.New("下载目标必须是已启用的本地存储")
	}
	root, err := filepath.EvalSymlinks(l.GetRootPath())
	if err != nil {
		return nil, "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(actual, "/"))))
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, "", errors.New("目标目录超出已挂载的 NAS 存储范围")
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", errors.New("请选择目标文件夹")
	}
	return d, full, nil
}
func BitQiuNASDirs(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, full, err := localNASDirectory(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	dirs := []nasTarget{}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, nasTarget{Path: e.Name(), Label: e.Name()})
		}
	}
	common.SuccessResp(c, dirs)
}
func BitQiuNASMkdir(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, full, err := localNASDirectory(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		common.ErrorStrResp(c, "请输入单个文件夹名称", 400)
		return
	}
	if err := os.Mkdir(filepath.Join(full, name), 0755); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c)
}
func BitQiuNASDownload(c *gin.Context) {
	var req MoveCopyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	src, err := user.JoinPath(req.SrcDir)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	source, _, err := op.GetStorageAndActualPath(src)
	if err != nil || source.GetStorage().Driver != "BitQiu" {
		common.ErrorStrResp(c, "请选择 BitQiu 存储中的文件", 400)
		return
	}
	dst, err := user.JoinPath(req.DstDir)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, destination, err := localNASDirectory(dst)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// Validate the whole selection before starting any tasks. Never allow this UI
	// to overwrite an existing destination or switch to a cloud write operation.
	if len(req.Names) == 0 || len(req.Names) > 50000 {
		common.ErrorStrResp(c, "请选择 1 至 50000 项", 400)
		return
	}
	seen := map[string]bool{}
	for _, name := range req.Names {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || seen[name] {
			common.ErrorStrResp(c, "文件选择包含无效或重复名称", 400)
			return
		}
		if _, err := utils.JoinUnderBase(src, name); err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		if _, err := os.Lstat(filepath.Join(destination, name)); err == nil {
			common.ErrorStrResp(c, "目标中已存在同名文件或文件夹，请选择新的目录", 409)
			return
		} else if !os.IsNotExist(err) {
			common.ErrorResp(c, err, 400)
			return
		}
		seen[name] = true
	}
	if req.Overwrite || req.SkipExisting {
		common.ErrorResp(c, fmt.Errorf("NAS 下载不覆盖已有目标，请选择新的目录"), 400)
		return
	}
	if err := queueNASSelection(c, req, destination); err != nil {
		common.ErrorResp(c, err, 400)
	}
}
