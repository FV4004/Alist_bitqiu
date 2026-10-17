package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/drivers/local"
	"github.com/alist-org/alist/v3/internal/model"
	alistnet "github.com/alist-org/alist/v3/internal/net"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/http_range"
)

var errNASRestart = errors.New("下载源无法安全续传，临时文件已保留；请确认后从头重新下载")

func nasPart(r NASRecord) string {
	return path.Join(path.Dir(r.Relative), "."+path.Base(r.Relative)+".alist-bitqiu-"+r.ID+".part")
}
func nasRoot(r NASRecord) (*os.Root, error) {
	d, err := op.GetStorageByMountPath(r.DstMount)
	if err != nil {
		return nil, err
	}
	l, ok := d.(*local.Local)
	if !ok || l.Disabled || filepath.Clean(l.GetRootPath()) != filepath.Clean(r.Root) {
		return nil, errors.New("NAS 存储挂载已改变，请恢复原挂载后继续")
	}
	if !filepath.IsLocal(filepath.FromSlash(r.Relative)) {
		return nil, errors.New("无效 NAS 目标路径")
	}
	return os.OpenRoot(r.Root)
}
func removeNASPartial(n *nasTask) error {
	n.mu.Lock()
	r := n.record
	n.mu.Unlock()
	root, err := nasRoot(r)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(nasPart(r))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func transferNAS(ctx context.Context, n *nasTask) error {
	n.mu.Lock()
	r := n.record
	n.mu.Unlock()
	root, err := nasRoot(r)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	if r.IsDir {
		return root.MkdirAll(r.Relative, 0755)
	}
	if err = root.MkdirAll(path.Dir(r.Relative), 0755); err != nil {
		return err
	}
	if err = NASValidateTarget(r); err != nil {
		return err
	}

	source, err := op.GetStorageByMountPath(r.SrcMount)
	if err != nil {
		return err
	}
	obj, err := op.Get(ctx, source, r.SrcPath)
	if err != nil {
		return fmt.Errorf("获取源文件失败：%w", err)
	}
	partial := nasPart(r)
	if info, e := root.Lstat(partial); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("临时文件类型异常，已停止")
	}
	file, err := root.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	offset := info.Size()
	if r.Restart {
		if err = file.Truncate(0); err != nil {
			return err
		}
		offset = 0
		n.mu.Lock()
		n.record.Restart = false
		n.record.RestartRequired = false
		n.record.Validator = ""
		n.record.ValidatorKind = ""
		n.record.Modified = obj.ModTime()
		if model.ObjectSizeKnown(obj) {
			n.record.TotalBytes = obj.GetSize()
		} else {
			n.record.TotalBytes = -1
		}
		r = n.record
		n.mu.Unlock()
	}
	if offset > 0 && ((model.ObjectSizeKnown(obj) && r.TotalBytes >= 0 && obj.GetSize() != r.TotalBytes) || (!r.Modified.IsZero() && !obj.ModTime().IsZero() && !r.Modified.Equal(obj.ModTime()))) {
		return errNASRestart
	}
	n.mu.Lock()
	n.record.TransferredBytes = offset
	n.mu.Unlock()
	if r.TotalBytes < 0 || offset != r.TotalBytes {
		if r.TotalBytes >= 0 && offset > r.TotalBytes {
			return errNASRestart
		}
		link, _, e := op.Link(ctx, source, r.SrcPath, model.LinkArgs{Header: http.Header{}})
		if e != nil {
			return fmt.Errorf("获取下载地址失败：%w", e)
		}
		reader, total, validator, kind, e := openNASSource(ctx, link, offset, r)
		if e != nil {
			return e
		}
		defer reader.Close()
		n.mu.Lock()
		if total >= 0 {
			n.record.TotalBytes = total
		}
		if validator != "" {
			n.record.Validator = validator
			n.record.ValidatorKind = kind
		}
		n.mu.Unlock()
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		buffer := make([]byte, 128*1024)
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			count, readErr := reader.Read(buffer)
			if count > 0 {
				written, writeErr := file.Write(buffer[:count])
				n.meter.Add(written, time.Now())
				n.mu.Lock()
				n.record.TransferredBytes += int64(written)
				n.mu.Unlock()
				checkpointNASDownloads()
				if writeErr != nil {
					return fmt.Errorf("NAS 写入失败：%w", writeErr)
				}
				if written != count {
					return io.ErrShortWrite
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				var requestErr *url.Error
				if errors.As(readErr, &requestErr) {
					readErr = requestErr.Err
				}
				return fmt.Errorf("下载连接中断，临时文件已保留：%w", readErr)
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	n.mu.Lock()
	total, received := n.record.TotalBytes, n.record.TransferredBytes
	n.mu.Unlock()
	if total >= 0 && received != total {
		return fmt.Errorf("文件接收不完整（已接收 %d，预期 %d），可开始任务续传", received, total)
	}
	if total < 0 {
		n.mu.Lock()
		n.record.TotalBytes = received
		n.mu.Unlock()
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = publishNASFile(ctx, root, partial, r.Relative, n); err != nil {
		return err
	}
	if !obj.ModTime().IsZero() {
		_ = root.Chtimes(r.Relative, obj.ModTime(), obj.ModTime())
	}
	return nil
}
func openNASSource(ctx context.Context, link *model.Link, offset int64, r NASRecord) (io.ReadCloser, int64, string, string, error) {
	if link.MFile != nil {
		if _, err := link.MFile.Seek(offset, io.SeekStart); err != nil {
			_ = link.MFile.Close()
			return nil, 0, "", "", err
		}
		return link.MFile, r.TotalBytes, "", "", nil
	}
	if link.RangeReadCloser != nil {
		reader, err := link.RangeReadCloser.RangeRead(ctx, http_range.Range{Start: offset, Length: -1})
		return reader, r.TotalBytes, "", "", err
	}
	if link.URL == "" {
		return nil, 0, "", "", errors.New("下载源没有可读取地址")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", link.URL, nil)
	if err != nil {
		return nil, 0, "", "", errors.New("下载地址无效")
	}
	request.Header = link.Header.Clone()
	if request.Header == nil {
		request.Header = http.Header{}
	}
	request.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if r.Validator != "" {
			request.Header.Set("If-Range", r.Validator)
		}
	}
	response, err := alistnet.HttpClient().Do(request)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, 0, "", "", fmt.Errorf("下载连接失败：%w", err)
	}
	fail := func(err error) (io.ReadCloser, int64, string, string, error) {
		_ = response.Body.Close()
		return nil, 0, "", "", err
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return fail(errors.New("下载源返回了无法安全续传的压缩内容"))
	}
	total := response.ContentLength
	if offset > 0 {
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return fail(errNASRestart)
		}
		if response.StatusCode != http.StatusPartialContent {
			return fail(fmt.Errorf("下载源 HTTP %d", response.StatusCode))
		}
		var start, end, size int64
		if _, e := fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &size); e != nil || start != offset || end < start || size <= end || (r.TotalBytes >= 0 && r.TotalBytes != size) {
			return fail(errNASRestart)
		}
		if r.Validator != "" && response.Header.Get(r.ValidatorKind) != "" && response.Header.Get(r.ValidatorKind) != r.Validator {
			return fail(errNASRestart)
		}
		total = size
	} else if response.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("下载源 HTTP %d", response.StatusCode))
	}
	if offset == 0 && r.TotalBytes >= 0 && total >= 0 && r.TotalBytes != total {
		return fail(errors.New("源文件大小已变化，请重新选择文件创建任务"))
	}
	if total < 0 {
		total = r.TotalBytes
	}
	validator, kind := response.Header.Get("ETag"), "ETag"
	if validator == "" || strings.HasPrefix(validator, "W/") {
		validator, kind = response.Header.Get("Last-Modified"), "Last-Modified"
	}
	return response.Body, total, validator, kind, nil
}
func publishNASFile(ctx context.Context, root *os.Root, partial, final string, n *nasTask) error {
	// The short final commit and control intent share a lock: once the complete
	// file is published, a late cancel must skip it rather than mislabel success.
	n.mu.Lock()
	if err := ctx.Err(); err != nil {
		n.mu.Unlock()
		return err
	}
	if n.record.Replace {
		version, info, err := NASPathVersion(root, final)
		if err != nil || !info.Mode().IsRegular() || version != n.record.TargetVersion {
			n.mu.Unlock()
			return errors.New("目标文件在确认后发生变化，旧文件未替换；请重新提交并确认")
		}
		// Same-directory rename replaces atomically; never remove the old file first.
		if err = root.Rename(partial, final); err != nil {
			n.mu.Unlock()
			return fmt.Errorf("替换失败，旧文件及临时下载已保留：%w", err)
		}
		n.record.State = "completed"
		n.mu.Unlock()
		return nil
	}
	linkErr := root.Link(partial, final)
	if linkErr == nil {
		n.record.State = "completed"
		n.mu.Unlock()
		cleanupNASPublished(root, partial, n)
		return nil
	}
	n.mu.Unlock()
	if os.IsExist(linkErr) {
		return errors.New("目标文件已存在，未覆盖")
	}
	// Filesystems without hard links use exclusive creation, never an overwriting
	// rename. Keep the partial on any failure so a retry can finish safely.
	in, err := root.Open(partial)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = out.Close()
		if !complete {
			_ = root.Remove(final)
		}
	}()
	_, err = io.Copy(out, &nasContextReader{ctx: ctx, Reader: in})
	if err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	n.mu.Lock()
	if err = ctx.Err(); err != nil {
		n.mu.Unlock()
		return err
	}
	complete = true
	n.record.State = "completed"
	n.mu.Unlock()
	cleanupNASPublished(root, partial, n)
	return nil
}

func cleanupNASPublished(root *os.Root, partial string, n *nasTask) {
	if err := root.Remove(partial); err != nil && !os.IsNotExist(err) {
		n.mu.Lock()
		n.record.Error = "文件已完整下载，但临时文件清理失败，请检查 NAS 权限"
		n.mu.Unlock()
	}
}

type nasContextReader struct {
	ctx context.Context
	io.Reader
}

func (r *nasContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
