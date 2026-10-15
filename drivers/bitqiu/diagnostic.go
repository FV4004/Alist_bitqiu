package bitqiu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const diagnosticEndpoint = "/cfi/fs/dir/resources"

// Triggered only by the one-shot diagnose_http action. No login, refresh or SMS
// requests are made. Both clients submit the same existing session and fields for the root and
// up to three pages of the first folder found on the root page.
func (d *BitQiu) diagnoseHTTP(ctx context.Context) error {
	if d.state.Token == "" || d.state.OpenID == "" || d.state.UserID == "" {
		return errors.New("BitQiu 诊断未执行：缺少完整已保存会话；未重新登录或发送短信")
	}
	rootID := d.resolveParentID(nil)
	resp, rootSummary := d.diagnosticCompare(ctx, rootID, "")
	childSummary := "skipped_root_response"
	if resp.Code == successCode && len(resp.Data) != 0 {
		objects, _, err := parseClientPage(resp.Data, rootID, "/", d.pageSize())
		if err != nil {
			childSummary = "metadata_unrecognized"
		} else {
			childSummary = "none_on_first_page"
			for _, object := range objects {
				if !object.IsDir() {
					continue
				}
				folder := object.(*Object)
				result := d.diagnosticPages(ctx, folder.GetID())
				childSummary = fmt.Sprintf("id_source=%s,legacy_id_diff=%t;%s", folder.ClientIDSource, folder.ClientIDChanged, result)
				break
			}
		}
	}
	// Only fixed labels, counts and numeric codes are reported. Names, IDs,
	// credentials, raw responses and wget output never enter the summary.
	return fmt.Errorf("BitQiu 诊断完成（未重新登录或发送短信）：root{%s}；child{%s}；操作已恢复 none，请重新打开编辑页保存恢复加载", rootSummary, childSummary)
}

// Bounded to three child pages: enough to inspect the transition from the
// successful first page without scanning the entire account or exposing cursors.
func (d *BitQiu) diagnosticPages(ctx context.Context, parentID string) string {
	cursor := ""
	var summaries []string
	for page := 1; page <= 3; page++ {
		resp, summary := d.diagnosticCompare(ctx, parentID, cursor)
		prefix := fmt.Sprintf("page%d{%s", page, summary)
		if resp.Code != successCode || len(resp.Data) == 0 {
			summaries = append(summaries, prefix+";stop=request_failed}")
			break
		}
		objects, more, err := parseClientPage(resp.Data, parentID, "/", d.pageSize())
		if err != nil {
			summaries = append(summaries, prefix+";stop=metadata_unrecognized}")
			break
		}
		prefix += fmt.Sprintf(";entries=%d;more=%t", len(objects), more)
		if !more || len(objects) == 0 {
			summaries = append(summaries, prefix+";stop=complete}")
			break
		}
		next, err := clientPageCursor(objects, cursor)
		if err != nil {
			summaries = append(summaries, prefix+";stop=cursor_missing_or_repeated}")
			break
		}
		suffix := ""
		if page == 3 {
			suffix = ";stop=diagnostic_limit"
		}
		summaries = append(summaries, prefix+fmt.Sprintf(";cursor_len=%d%s}", len(next), suffix))
		cursor = next
	}
	return strings.Join(summaries, ";")
}

// Each pair uses identical fields and the same saved session, without refresh.
func (d *BitQiu) diagnosticCompare(ctx context.Context, parentID, cursor string) (clientResponse, string) {
	fields := url.Values{
		"parent_id": {parentID}, "limit": {strconv.Itoa(d.pageSize())},
		"sort": {cursor}, "desc": {d.orderDesc()}, "order": {d.orderType()}, "model": {"1"},
		"access_token": {d.state.Token}, "open_id": {d.state.OpenID}, "user_id": {d.state.UserID},
	}
	goCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resp, err := d.clientRequest(goCtx, diagnosticEndpoint, fields, false)
	cancel()
	native := "transport_error"
	if err == nil {
		native = fmt.Sprintf("HTTP=200,code=%s", resp.Code)
	} else {
		var h *clientHTTPError
		if errors.As(err, &h) {
			native = fmt.Sprintf("HTTP=%d,code=%s,response=%s,bytes=%d", h.Status, diagnosticCode(h.Code), h.Kind, h.BodyBytes)
		}
	}
	form := url.Values{}
	for k, v := range fields {
		form[k] = append([]string(nil), v...)
	}
	form.Set("app_id", clientAppID)
	form.Set("platform", d.ClientPlatform)
	form.Set("org_channel", d.ClientChannel)
	host := d.apiBase
	if host == "" {
		host = clientBaseURL
	}
	wget := diagnosticWget(ctx, host+diagnosticEndpoint, form.Encode())
	return resp, fmt.Sprintf("Go[%s]；wget[%s]", native, wget)
}

func diagnosticCode(code string) string {
	if len(code) == 0 || len(code) > 10 || strings.IndexFunc(code, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "none"
	}
	return code
}

var diagnosticHTTPStatus = regexp.MustCompile(`(?m)^\s*HTTP/[0-9.]+\s+([0-9]{3})(?:\s|$)`)

// Credential form and response files exist only within a private temporary
// directory and are removed before returning. Credentials never appear in argv.
func diagnosticWget(ctx context.Context, target, form string) string {
	wget, err := exec.LookPath("wget")
	if err != nil {
		return "unavailable"
	}
	dir, err := os.MkdirTemp("", "alist-bitqiu-diag-")
	if err != nil {
		return "tempfile_error"
	}
	defer os.RemoveAll(dir)
	formFile := filepath.Join(dir, "form")
	responseFile := filepath.Join(dir, "response")
	if os.WriteFile(formFile, []byte(form), 0600) != nil || os.WriteFile(responseFile, nil, 0600) != nil {
		return "tempfile_error"
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(requestCtx, wget, "-S", "-O", responseFile, "-T", "15", "-U", clientUA, "--header", "Content-Type: application/x-www-form-urlencoded", "--post-file", formFile, target)
	var headers diagnosticBuffer
	cmd.Stderr = &headers
	cmd.Stdout = io.Discard
	runErr := cmd.Run()
	matches := diagnosticHTTPStatus.FindAllSubmatch(headers.data, -1)
	if len(matches) == 0 {
		if requestCtx.Err() != nil {
			return "timeout"
		}
		return "transport_error"
	}
	status, _ := strconv.Atoi(string(matches[len(matches)-1][1]))
	f, err := os.Open(responseFile)
	if err != nil {
		return fmt.Sprintf("HTTP=%d,response=unreadable", status)
	}
	defer f.Close()
	const maxBody = 2 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(f, maxBody+1))
	if err != nil {
		return fmt.Sprintf("HTTP=%d,response=unreadable", status)
	}
	if len(body) > maxBody {
		return fmt.Sprintf("HTTP=%d,response=oversize", status)
	}
	kind := "other"
	code := "none"
	var envelope map[string]json.RawMessage
	if len(body) == 0 {
		kind = "empty"
	} else if json.Unmarshal(body, &envelope) == nil {
		kind = "JSON"
		code = diagnosticCode(rawText(envelope["code"]))
	} else {
		lower := bytes.ToLower(bytes.TrimSpace(body))
		if bytes.HasPrefix(lower, []byte("<html")) || bytes.HasPrefix(lower, []byte("<!doctype html")) {
			kind = "HTML"
		}
	}
	// BusyBox may discard error bodies. Do not claim a missing body was sent by
	// the server when wget itself failed with a non-2xx status.
	if runErr != nil && len(body) == 0 {
		kind = "unavailable"
	}
	return fmt.Sprintf("HTTP=%d,code=%s,response=%s,bytes=%d", status, code, kind, len(body))
}

type diagnosticBuffer struct{ data []byte }

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	const limit = 16 * 1024
	if len(b.data) < limit {
		n := len(p)
		if n > limit-len(b.data) {
			n = limit - len(b.data)
		}
		b.data = append(b.data, p[:n]...)
	}
	return len(p), nil
}
