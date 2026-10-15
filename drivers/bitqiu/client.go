package bitqiu

// Native protocol reference: BitqiuPan.exe 4.1.0.1, SHA256
// 57c5d6970920ffcfc3f6b3f323327080be60676940b41daa7b43acd97129bff2.
// Login 0x1400150a0; SMS 0x14001f420; validation 0x1400160f0;
// directory listing /cfi/fs/dir/resources; download 0x140021e10.
import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/google/uuid"
)

const clientBaseURL = "https://pan-api.bitqiu.com"
const clientAppID = "d0beea3fe1a1423392690dfd4d3713fd"

// An application protocol constant embedded in the distributed Windows client,
// not an account credential. The native implementation appends this verbatim.
const clientSigningKey = "ZWtVdHB0WjBocFIxdmNzZlg3cUQ5SFhEYU5pWGsv"
const clientUA = "curl/3C48359D-C0B2-4C09-A7A7-608910AA4D62"
const challengeLifetime = 15 * time.Minute

type clientState struct {
	Binding        string `json:"binding"`
	Token          string `json:"token,omitempty"`
	OpenID         string `json:"open_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	TempToken      string `json:"temp_token,omitempty"`
	Phone          string `json:"phone,omitempty"`
	ChallengedAt   int64  `json:"challenged_at,omitempty"`
	SMSAttemptedAt int64  `json:"sms_attempted_at,omitempty"`
}

type clientResponse struct {
	Code string
	Data json.RawMessage
}

func (d *BitQiu) windowsMode() bool { return d.LoginMode != "web" }
func md5Text(s string) string       { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }

// The signature uses unescaped field values in lexical key order. HTTP form
// encoding is applied only after signing, matching native field construction.
func signClientForm(fields url.Values) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "sign" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+fields.Get(k))
	}
	return md5Text(strings.Join(parts, "&") + clientSigningKey)
}

func (d *BitQiu) binding() string {
	sum := sha256.Sum256([]byte(d.Username + "\x00" + d.Password + "\x00" + d.ClientPlatform + "\x00" + d.ClientChannel))
	return hex.EncodeToString(sum[:])
}
func (d *BitQiu) saveClient() {
	b, _ := json.Marshal(d.state)
	d.ClientState = string(b)
	if d.Storage.ID != 0 {
		op.MustSaveDriverStorage(d)
	}
}
func (d *BitQiu) clearChallenge() {
	d.state.TempToken = ""
	d.state.Phone = ""
	d.state.ChallengedAt = 0
	d.state.SMSAttemptedAt = 0
}
func (d *BitQiu) pending() error {
	return fmt.Errorf("%w：BitQiu 需要新设备短信验证；请重新打开已创建的存储编辑页，收到短信后填写 sms_code 并保存；未收到可选择 send_sms 重发", errs.DriverInitPending)
}
func (d *BitQiu) initWindows(ctx context.Context) error {
	d.clientMu.Lock()
	defer d.clientMu.Unlock()
	if d.LoginMode != "" && d.LoginMode != "windows" {
		return errors.New("unsupported BitQiu login_mode")
	}
	if d.client == nil {
		d.client = base.NewRestyClient()
	}
	// Never retry an SMS after a transport failure: it may already have been sent.
	d.client.SetRetryCount(0)
	// Native platform layout: app ID;version;MAC;OS;computer name;pc.
	// The generated local device identity stays stable across NAS restarts.
	if d.ClientPlatform == "" {
		id := uuid.New()
		mac := fmt.Sprintf("02:%02X:%02X:%02X:%02X:%02X", id[0], id[1], id[2], id[3], id[4])
		d.ClientPlatform = clientAppID + ";4.1.0.1;" + mac + ";Windows 10(64);ALIST-NAS;pc"
	}
	if d.ClientChannel == "" {
		d.ClientChannel = "pc"
	}
	if d.ClientState != "" {
		if err := json.Unmarshal([]byte(d.ClientState), &d.state); err != nil {
			return errors.New("BitQiu client_state 无效，请清空后重新登录")
		}
	}
	binding := d.binding()
	if d.state.Binding != binding {
		d.state = clientState{Binding: binding}
	}
	action := d.LoginAction
	code := strings.TrimSpace(d.SMSCode)
	// Consume before doing any I/O. An application restart cannot resend an SMS
	// or reuse a code merely because a saved action was left in the database.
	d.LoginAction = "none"
	d.SMSCode = ""
	if action == "reset_login" {
		d.state = clientState{Binding: binding}
	}
	d.saveClient()
	if action != "" && action != "none" && action != "send_sms" && action != "verify_sms" && action != "reset_login" && action != "diagnose_http" {
		return errors.New("unsupported BitQiu login_action")
	}
	if action == "diagnose_http" {
		return d.diagnoseHTTP(ctx)
	}
	if d.state.TempToken != "" && time.Since(time.Unix(d.state.ChallengedAt, 0)) > challengeLifetime {
		d.clearChallenge()
		d.saveClient()
		if action == "verify_sms" {
			return errors.New("BitQiu 验证会话已过期，请重新登录并发送验证码")
		}
	}
	// A new driver must start password login before it can request or verify an SMS.
	// Ignore premature verification fields when no challenge exists yet.
	if d.state.TempToken == "" && action == "verify_sms" {
		action, code = "none", ""
	}
	if d.state.TempToken != "" && (action == "none" || action == "") && code != "" {
		action = "verify_sms"
	}
	if action == "verify_sms" {
		if code == "" {
			return errors.New("请填写 sms_code 后再提交验证")
		}
		if d.state.TempToken == "" {
			return errors.New("没有待验证的登录会话；请先选择 send_sms 保存并重新打开编辑页")
		}
		form := url.Values{"captcha": {code}, "phone": {d.state.Phone}, "temp_token": {d.state.TempToken}}
		resp, err := d.clientRequest(ctx, "/loginServer/validatePhone", form, true)
		if err != nil {
			return err
		}
		if resp.Code != successCode {
			return fmt.Errorf("BitQiu 验证未通过（code=%s）；请重新输入验证码，或重新登录后发送", resp.Code)
		}
		if err = d.acceptClientSession(resp.Data); err != nil {
			return err
		}
		return d.checkClientSession(ctx)
	}
	if d.state.Token != "" {
		err := d.checkClientSession(ctx)
		if err == nil {
			return nil
		}
		var expired *clientAuthError
		if !errors.As(err, &expired) {
			return err
		}
		d.state.Token = ""
		d.state.OpenID = ""
		d.state.UserID = ""
		d.saveClient()
	}
	if d.state.TempToken == "" {
		if err := d.passwordClientLogin(ctx); err != nil {
			return err
		}
		if d.state.Token != "" {
			return d.checkClientSession(ctx)
		}
	}
	// Send the first SMS only after driver initialization has obtained a challenge.
	// Persist the attempt before I/O so reloads and failures never resend it.
	if action == "send_sms" || d.state.SMSAttemptedAt == 0 {
		if d.state.SMSAttemptedAt != 0 && time.Since(time.Unix(d.state.SMSAttemptedAt, 0)) < time.Minute {
			return errors.New("BitQiu 发送间隔不足 60 秒，请稍后再选择 send_sms 保存")
		}
		d.state.SMSAttemptedAt = time.Now().Unix()
		d.saveClient()
		resp, err := d.clientRequest(ctx, "/loginServer/getMobileAuthCode", url.Values{"phone": {d.state.Phone}, "temp_token": {d.state.TempToken}, "type": {"5"}}, true)
		if err != nil {
			return err
		}
		if resp.Code != successCode {
			return fmt.Errorf("BitQiu 短信发送未成功（code=%s）；可等待 60 秒后重新选择 send_sms", resp.Code)
		}
		return fmt.Errorf("%w：BitQiu 新设备短信已发送；请重新打开已创建的存储编辑页，填写 sms_code 并保存", errs.DriverInitPending)
	}
	return d.pending()
}

func (d *BitQiu) passwordClientLogin(ctx context.Context) error {
	resp, err := d.clientRequest(ctx, "/loginServer/login", url.Values{"passport": {d.Username}, "password": {md5Text(d.Password)}}, true)
	if err != nil {
		return err
	}
	switch resp.Code {
	case successCode:
		return d.acceptClientSession(resp.Data)
	case "10807":
		obj, err := decodeObject(resp.Data)
		if err != nil {
			return errors.New("BitQiu 设备验证响应格式无效")
		}
		token := textField(obj, "temp_token", "tempToken")
		phone := textField(obj, "phone")
		if token == "" {
			return errors.New("BitQiu 设备验证响应缺少 temp_token")
		}
		d.state.Token = ""
		d.state.OpenID = ""
		d.state.UserID = ""
		d.state.TempToken = token
		d.state.Phone = phone
		d.state.ChallengedAt = time.Now().Unix()
		d.state.SMSAttemptedAt = 0
		d.saveClient()
		return nil
	case "10806":
		return errors.New("BitQiu 账号需要先绑定手机号，请在官方客户端完成绑定后重试")
	default:
		return fmt.Errorf("BitQiu 客户端登录失败（code=%s）；请核对账号密码、设备标识及账号状态", resp.Code)
	}
}
func (d *BitQiu) acceptClientSession(raw json.RawMessage) error {
	obj, err := decodeObject(raw)
	if err != nil {
		return errors.New("BitQiu 登录响应格式无效")
	}
	token := textField(obj, "token", "access_token")
	open := textField(obj, "open_id", "openId")
	user := textField(obj, "user_id", "userId")
	if token == "" || open == "" || user == "" || user == "0" {
		return errors.New("BitQiu 登录响应缺少 token/open_id/user_id，请重置登录后重试")
	}
	d.state.Token = token
	d.state.OpenID = open
	d.state.UserID = user
	d.clearChallenge()
	d.userID = user
	d.saveClient()
	return nil
}
func (d *BitQiu) clientRequest(ctx context.Context, endpoint string, fields url.Values, signed bool) (clientResponse, error) {
	var out clientResponse
	if d.client == nil {
		return out, errors.New("BitQiu client not initialized")
	}
	form := url.Values{}
	for k, v := range fields {
		form[k] = append([]string(nil), v...)
	}
	form.Set("app_id", clientAppID)
	form.Set("platform", d.ClientPlatform)
	form.Set("org_channel", d.ClientChannel)
	if signed {
		form.Set("timestamp", strconv.FormatInt(time.Now().Unix()*1000, 10))
		form.Set("sign", signClientForm(form))
	}
	host := d.apiBase
	if host == "" {
		host = clientBaseURL
	}
	resp, err := d.client.R().SetContext(ctx).SetHeader("User-Agent", clientUA).SetHeader("Content-Type", "application/x-www-form-urlencoded").SetBody(form.Encode()).Post(host + endpoint)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("BitQiu API 网络请求失败，请检查 NAS 网络连接")
	}
	var envelope map[string]json.RawMessage
	jsonErr := json.Unmarshal(resp.Body(), &envelope)
	out.Code = rawText(envelope["code"])
	validCode := out.Code != "" && len(out.Code) <= 10 && strings.IndexFunc(out.Code, func(r rune) bool { return r < '0' || r > '9' }) < 0
	if !validCode {
		out.Code = ""
	}
	if resp.StatusCode() != http.StatusOK {
		kind := "other"
		if jsonErr == nil {
			kind = "JSON"
		} else if strings.Contains(strings.ToLower(resp.Header().Get("Content-Type")), "html") {
			kind = "HTML"
		}
		// Never include the response body/message, headers or submitted fields:
		// upstream errors can echo passwords, phone numbers or session tokens.
		return out, &clientHTTPError{Status: resp.StatusCode(), Endpoint: endpoint, Code: out.Code, Kind: kind, BodyBytes: len(resp.Body())}
	}
	if jsonErr != nil {
		return clientResponse{}, errors.New("BitQiu API 返回无效 JSON")
	}
	if !validCode {
		return clientResponse{}, errors.New("BitQiu API 返回无效业务码")
	}
	out.Data = envelope["data"]
	return out, nil
}

// A 423 alone does not establish an expired session or a locked directory.
// Keep only safe metadata for diagnosis, and require an explicit auth code.
type clientHTTPError struct {
	Status               int
	BodyBytes            int
	Endpoint, Code, Kind string
}

func (e *clientHTTPError) Error() string {
	code := e.Code
	if code == "" {
		code = "none"
	}
	return fmt.Sprintf("BitQiu API HTTP %d (endpoint=%s, code=%s, response=%s, bytes=%d)", e.Status, e.Endpoint, code, e.Kind, e.BodyBytes)
}
func clientHTTPAuthError(err error, code string) bool {
	var httpErr *clientHTTPError
	return errors.As(err, &httpErr) && (httpErr.Status == 401 || httpErr.Status == 403 || httpErr.Status == 423) && isClientAuthCode(code)
}

type clientAuthError struct{}

func (*clientAuthError) Error() string { return "BitQiu 登录已过期" }

// 10401 is also used for invalid parameters; it must not discard the saved session.
func isClientAuthCode(code string) bool { return code == "10404" || code == "20003" }
func (d *BitQiu) authenticatedClientRequest(ctx context.Context, endpoint string, fields url.Values) (clientResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if d.state.Token == "" {
			return clientResponse{}, d.pending()
		}
		form := url.Values{}
		for k, v := range fields {
			form[k] = v
		}
		form.Set("access_token", d.state.Token)
		form.Set("open_id", d.state.OpenID)
		form.Set("user_id", d.state.UserID)
		resp, err := d.clientRequest(ctx, endpoint, form, false)
		if err != nil && !clientHTTPAuthError(err, resp.Code) {
			return resp, err
		}
		if !isClientAuthCode(resp.Code) {
			return resp, nil
		}
		d.state.Token = ""
		d.state.OpenID = ""
		d.state.UserID = ""
		d.saveClient()
		if attempt == 1 {
			return resp, &clientAuthError{}
		}
		if err = d.passwordClientLogin(ctx); err != nil {
			return resp, err
		}
		if d.state.Token == "" {
			return resp, d.pending()
		}
	}
	return clientResponse{}, &clientAuthError{}
}
func (d *BitQiu) checkClientSession(ctx context.Context) error {
	form := url.Values{"access_token": {d.state.Token}, "open_id": {d.state.OpenID}, "user_id": {d.state.UserID}}
	resp, err := d.clientRequest(ctx, "/user/get", form, false)
	if err != nil && !clientHTTPAuthError(err, resp.Code) {
		return err
	}
	if isClientAuthCode(resp.Code) {
		return &clientAuthError{}
	}
	if resp.Code != successCode {
		return fmt.Errorf("BitQiu 获取用户信息失败（code=%s）", resp.Code)
	}
	d.userID = d.state.UserID
	if d.Addition.GetRootId() == "" || d.Addition.GetRootId() == "0" {
		if obj, e := decodeObject(resp.Data); e == nil {
			if root := textField(obj, "rootDirId", "root_dir_id"); root != "" {
				d.Addition.RootFolderID = root
				d.saveClient()
			}
		}
	}
	return nil
}

func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var num json.Number
	if json.Unmarshal(raw, &num) == nil {
		return num.String()
	}
	return ""
}
func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	err := json.Unmarshal(raw, &out)
	if err == nil && out == nil {
		err = errors.New("expected JSON object")
	}
	return out, err
}
func textField(obj map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if s := rawText(obj[k]); s != "" {
			return s
		}
	}
	return ""
}
func intField(obj map[string]json.RawMessage, keys ...string) int64 {
	n, _ := strconv.ParseInt(textField(obj, keys...), 10, 64)
	return n
}

// Client responses have used both camelCase and snake_case metadata. Decode
// known resource containers explicitly; never search unrelated JSON for a URL.
func clientObject(raw json.RawMessage, parentID, parentPath string, folder bool) (model.Obj, error) {
	m, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	id := textField(m, "resourceId", "resource_id", "fileId", "file_id", "dirId", "dir_id", "id")
	name := textField(m, "name", "fileName", "file_name")
	if id == "" || name == "" {
		return nil, errors.New("BitQiu 文件元数据缺少 ID 或文件名")
	}
	typ := intField(m, "resourceType", "resource_type")
	o := &Object{Object: model.Object{ID: id, Name: name, IsFolder: folder || typ == 1, Size: intField(m, "size", "fileSize", "file_size")}, ParentID: parentID}
	o.ClientCursor = textField(m, "sort")
	o.SetPath(path.Join(parentPath, name))
	for _, entry := range []struct {
		keys     []string
		modified bool
	}{{[]string{"updateTime", "update_time"}, true}, {[]string{"createTime", "create_time"}, false}} {
		value := textField(m, entry.keys...)
		t := parseBitQiuTime(&value)
		if entry.modified {
			o.Modified = t
		} else {
			o.Ctime = t
		}
	}
	return o, nil
}
func parseClientPage(raw json.RawMessage, parentID, parentPath string, limit int) ([]model.Obj, bool, error) {
	var records []json.RawMessage
	var objs []model.Obj
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &records); err != nil {
			return nil, false, err
		}
		for _, r := range records {
			o, err := clientObject(r, parentID, parentPath, false)
			if err != nil {
				return nil, false, err
			}
			objs = append(objs, o)
		}
		return objs, len(records) == limit, nil
	}
	m, err := decodeObject(raw)
	if err != nil {
		return nil, false, err
	}
	found := false
	count := 0
	for _, key := range []string{"folders", "files", "resources", "list", "data"} {
		v, ok := m[key]
		if !ok || bytes.Equal(v, []byte("null")) {
			continue
		}
		if len(v) == 0 || v[0] != '[' {
			continue
		}
		// Alternative list containers are not cumulative with folders/files.
		if found && key != "files" {
			break
		}
		found = true
		if err = json.Unmarshal(v, &records); err != nil {
			return nil, false, err
		}
		for _, r := range records {
			o, e := clientObject(r, parentID, parentPath, key == "folders")
			if e != nil {
				return nil, false, e
			}
			objs = append(objs, o)
		}
		count += len(records)
	}
	if !found {
		return nil, false, errors.New("BitQiu 未识别的目录响应结构")
	}
	for _, key := range []string{"hasNext", "has_next"} {
		if v, ok := m[key]; ok {
			var next bool
			if json.Unmarshal(v, &next) == nil {
				return objs, next, nil
			}
		}
	}
	return objs, count == limit, nil
}
func (d *BitQiu) listWindows(ctx context.Context, dir model.Obj) ([]model.Obj, error) {
	d.clientMu.Lock()
	defer d.clientMu.Unlock()
	parentID := d.resolveParentID(dir)
	parentPath := ""
	if dir != nil {
		parentPath = dir.GetPath()
	}
	limit := d.pageSize()
	var all []model.Obj
	seen := map[string]bool{}
	cursor := ""
	for page := 1; page <= 10000; page++ {
		form := url.Values{"parent_id": {parentID}, "limit": {strconv.Itoa(limit)}, "sort": {cursor}, "desc": {d.orderDesc()}, "order": {d.orderType()}, "model": {"1"}}
		resp, err := d.authenticatedClientRequest(ctx, "/cfi/fs/dir/resources", form)
		if err != nil {
			return nil, err
		}
		if resp.Code != successCode {
			return nil, fmt.Errorf("BitQiu 列目录失败（code=%s）", resp.Code)
		}
		objs, more, err := parseClientPage(resp.Data, parentID, parentPath, limit)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, o := range objs {
			key := strconv.FormatBool(o.IsDir()) + ":" + o.GetID()
			if !seen[key] {
				seen[key] = true
				all = append(all, o)
				added++
			}
		}
		if !more || len(objs) == 0 {
			return all, nil
		}
		if added == 0 {
			return nil, errors.New("BitQiu 目录分页未前进，已终止以避免遗漏文件；请检查客户端 API 分页协议")
		}
		last := objs[len(objs)-1].(*Object).ClientCursor
		if last == "" || last == cursor {
			return nil, errors.New("BitQiu 目录缺少下一页 sort 游标，已终止以避免遗漏文件")
		}
		cursor = last
	}
	return nil, errors.New("BitQiu 目录超过分页上限")
}
func validDownloadURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
func parseClientDownload(raw json.RawMessage, wantID string) (string, error) {
	if s := rawText(raw); validDownloadURL(s) {
		return s, nil
	}
	var records []json.RawMessage
	if json.Unmarshal(raw, &records) == nil {
		var candidate string
		for _, r := range records {
			m, err := decodeObject(r)
			if err != nil {
				continue
			}
			id := textField(m, "fileId", "file_id", "resourceId", "resource_id", "id")
			if id != "" && id != wantID {
				continue
			}
			s := textField(m, "url", "downloadUrl", "download_url")
			if validDownloadURL(s) {
				if id == wantID {
					return s, nil
				}
				if candidate != "" {
					return "", errors.New("BitQiu 下载响应存在多个不明确的地址")
				}
				candidate = s
			}
		}
		if candidate != "" {
			return candidate, nil
		}
	} else if m, err := decodeObject(raw); err == nil {
		if id := textField(m, "fileId", "file_id", "resourceId", "resource_id", "id"); id != "" && id != wantID {
			return "", errors.New("BitQiu 下载响应的文件 ID 不匹配")
		}
		if s := textField(m, "url", "downloadUrl", "download_url"); validDownloadURL(s) {
			return s, nil
		}
		for _, key := range []string{"files", "list", "data"} {
			if v, ok := m[key]; ok {
				return parseClientDownload(v, wantID)
			}
		}
	}
	return "", errors.New("BitQiu 下载响应没有可用原文件地址")
}
func (d *BitQiu) linkWindows(ctx context.Context, file model.Obj) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	d.clientMu.Lock()
	defer d.clientMu.Unlock()
	resp, err := d.authenticatedClientRequest(ctx, "/cfi/fs/download/file/url", url.Values{"file_ids": {file.GetID()}, "dir_ids": {""}})
	if err != nil {
		return nil, err
	}
	if resp.Code != successCode {
		return nil, fmt.Errorf("BitQiu 获取下载地址失败（code=%s）", resp.Code)
	}
	target, err := parseClientDownload(resp.Data, file.GetID())
	if err != nil {
		return nil, err
	}
	// Tokens belong only to API requests, never to a third-party download host.
	return &model.Link{URL: target, Header: http.Header{"User-Agent": {clientUA}}}, nil
}
