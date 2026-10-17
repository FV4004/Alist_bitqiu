package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
)

// Versions stay server-side. Include inode/change time where available, without
// hashing multi-GB files or including access time (which reads may change).
func NASPathVersion(root *os.Root, relative string) (string, os.FileInfo, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		return "", nil, err
	}
	value := fmt.Sprintf("%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode())
	sys := reflect.ValueOf(info.Sys())
	if sys.Kind() == reflect.Pointer {
		sys = sys.Elem()
	}
	if sys.IsValid() && sys.Kind() == reflect.Struct {
		for _, key := range []string{"Dev", "Ino", "Ctim", "Ctimespec"} {
			field := sys.FieldByName(key)
			if field.IsValid() && field.CanInterface() {
				value += fmt.Sprint(field.Interface())
			}
		}
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:]), info, nil
}

// A directory collision is mergeable; symlinks, devices and file/directory
// mismatches are never covered by a blanket file-replace choice.
func NASCheckParents(root *os.Root, relative string) error {
	for dir := path.Dir(relative); dir != "." && dir != "/"; dir = path.Dir(dir) {
		info, err := root.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("目标父路径不是普通文件夹，请选择其他保存目录：" + strings.TrimPrefix(dir, "/"))
		}
	}
	return nil
}
func NASValidateTarget(r NASRecord) error {
	root, err := nasRoot(r)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = NASCheckParents(root, r.Relative); err != nil {
		return err
	}
	version, info, err := NASPathVersion(root, r.Relative)
	if os.IsNotExist(err) {
		if r.Replace {
			return errors.New("已确认的目标文件发生变化，请重新选择替换或跳过")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if r.IsDir && info.IsDir() {
		return nil
	}
	if !r.Replace || !info.Mode().IsRegular() || version != r.TargetVersion {
		return errors.New("目标文件已存在或发生变化，请重新检查后选择替换或跳过")
	}
	return nil
}
