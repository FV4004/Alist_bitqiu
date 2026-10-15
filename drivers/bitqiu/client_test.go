package bitqiu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/go-resty/resty/v2"
)

func testClient(t *testing.T, handler http.HandlerFunc) *BitQiu {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &BitQiu{Addition: Addition{Username: "user", Password: "password", ClientPlatform: "stable-device", ClientChannel: "pc", PageIntervalMS: "0"}, pageDelayWait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, apiBase: server.URL, client: resty.New()}
}
func cloneClient(d *BitQiu) *BitQiu {
	return &BitQiu{Addition: d.Addition, pageDelayWait: d.pageDelayWait, apiBase: d.apiBase, client: resty.New()}
}
func reply(w http.ResponseWriter, code interface{}, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": code, "data": data})
}
func sessionData() map[string]interface{} {
	return map[string]interface{}{"token": "account-secret", "open_id": "open", "user_id": 9007199254740993}
}

func TestDeviceVerificationSurvivesStorageReload(t *testing.T) {
	var login, sms, verify, user atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" {
			t.Error("client API must use POST")
		}
		if r.Form.Get("platform") != "stable-device" {
			t.Error("device identity changed")
		}
		switch r.URL.Path {
		case "/loginServer/login":
			login.Add(1)
			if r.Form.Get("password") != "5f4dcc3b5aa765d61d8327deb882cf99" {
				t.Error("password digest")
			}
			if r.Form.Get("sign") != signClientForm(r.Form) {
				t.Error("bad signature")
			}
			reply(w, 10807, map[string]interface{}{"temp_token": "challenge-secret", "phone": "13800000000"})
		case "/loginServer/getMobileAuthCode":
			sms.Add(1)
			if r.Form.Get("temp_token") != "challenge-secret" || r.Form.Get("type") != "5" {
				t.Error("challenge/type lost")
			}
			if r.Form.Get("sign") != signClientForm(r.Form) {
				t.Error("SMS signature")
			}
			reply(w, "10200", nil)
		case "/loginServer/validatePhone":
			verify.Add(1)
			if r.Form.Get("temp_token") != "challenge-secret" || r.Form.Get("captcha") != "123456" {
				t.Error("wrong verification session")
			}
			reply(w, 10200, sessionData())
		case "/user/get":
			user.Add(1)
			if r.Form.Get("access_token") != "account-secret" || r.Form.Get("user_id") != "9007199254740993" {
				t.Error("token or precise user ID lost")
			}
			reply(w, 10200, map[string]string{"rootDirId": "root"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if err := d.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "新设备") {
		t.Fatalf("expected challenge, got %v", err)
	}
	if sms.Load() != 1 || verify.Load() != 0 {
		t.Fatal("driver must send the first SMS after login without requiring a code")
	}
	if d.LoginAction != "none" {
		t.Fatal("action not consumed")
	}
	d = cloneClient(d)
	_ = d.Init(context.Background())
	if sms.Load() != 1 || login.Load() != 1 {
		t.Fatal("restart repeated SMS or password login")
	}
	d = cloneClient(d)
	d.LoginAction = "none" // filling the code after startup is sufficient
	d.SMSCode = "123456"
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.SMSCode != "" || d.state.TempToken != "" || d.state.Phone != "" {
		t.Fatal("one-time secrets retained")
	}
	if d.RootFolderID != "root" {
		t.Fatal("API root ID not resolved")
	}
	d = cloneClient(d)
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if verify.Load() != 1 || user.Load() != 2 || login.Load() != 1 || sms.Load() != 1 {
		t.Fatal("session not reused")
	}
}
func TestVerificationFailureConsumesCodeWithoutExposingSecrets(t *testing.T) {
	var calls atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(w, 10805, map[string]string{"message": "challenge-secret"})
	})
	d.state = clientState{Binding: d.binding(), TempToken: "challenge-secret", Phone: "13800000000", ChallengedAt: time.Now().Unix(), SMSAttemptedAt: time.Now().Unix()}
	d.saveClient()
	d.LoginAction = "verify_sms"
	d.SMSCode = "bad-code"
	err := d.Init(context.Background())
	if err == nil || strings.Contains(err.Error(), "challenge-secret") {
		t.Fatalf("unexpected error %v", err)
	}
	if d.LoginAction != "none" || d.SMSCode != "" {
		t.Fatal("action/code not consumed")
	}
	d = cloneClient(d)
	_ = d.Init(context.Background())
	if calls.Load() != 1 {
		t.Fatal("restart resubmitted validation")
	}
}
func TestSMSRateLimitPersistsAcrossReload(t *testing.T) {
	var sends atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) { sends.Add(1); reply(w, 10200, nil) })
	d.state = clientState{Binding: d.binding(), TempToken: "challenge", ChallengedAt: time.Now().Unix()}
	d.saveClient()
	d.LoginAction = "send_sms"
	_ = d.Init(context.Background())
	d = cloneClient(d)
	d.LoginAction = "send_sms"
	err := d.Init(context.Background())
	if err == nil || !strings.Contains(err.Error(), "60") {
		t.Fatalf("rate limit: %v", err)
	}
	if sends.Load() != 1 {
		t.Fatal("SMS rate limit lost on reload")
	}
}
func TestExpiredChallengeDoesNotSubmitOldOTP(t *testing.T) {
	var calls atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	d.state = clientState{Binding: d.binding(), TempToken: "old", ChallengedAt: time.Now().Add(-time.Hour).Unix()}
	d.saveClient()
	d.LoginAction = "verify_sms"
	d.SMSCode = "123456"
	if err := d.Init(context.Background()); err == nil {
		t.Fatal("expired challenge accepted")
	}
	if calls.Load() != 0 || d.state.TempToken != "" {
		t.Fatal("expired OTP submitted")
	}
}
func TestAccountOrDeviceChangeInvalidatesSavedSession(t *testing.T) {
	for _, change := range []string{"username", "password", "platform"} {
		t.Run(change, func(t *testing.T) {
			var login atomic.Int32
			d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/loginServer/getMobileAuthCode" {
					reply(w, 10200, nil)
					return
				}
				if r.URL.Path != "/loginServer/login" {
					t.Errorf("stale session used at %s", r.URL.Path)
				}
				login.Add(1)
				reply(w, 10807, map[string]string{"temp_token": "new", "phone": "13800000000"})
			})
			d.state = clientState{Binding: d.binding(), Token: "old", OpenID: "old", UserID: "1"}
			d.saveClient()
			switch change {
			case "username":
				d.Username = "other"
			case "password":
				d.Password = "other"
			case "platform":
				d.ClientPlatform = "other"
			}
			_ = d.Init(context.Background())
			if login.Load() != 1 || d.state.Token != "" || d.state.TempToken != "new" {
				t.Fatal("old credentials retained")
			}
		})
	}
}
func TestCursorPaginationAndTokenRefresh(t *testing.T) {
	var pages, logins atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/loginServer/login":
			logins.Add(1)
			reply(w, 10200, sessionData())
		case "/cfi/fs/dir/resources":
			pages.Add(1)
			if r.Form.Get("access_token") == "expired" {
				reply(w, 10404, nil)
				return
			}
			if r.Form.Get("parent_id") != "root" {
				t.Error("wrong parent")
			}
			cursor := r.Form.Get("sort")
			if cursor == "" {
				reply(w, 10200, map[string]interface{}{"hasNext": true, "data": []map[string]interface{}{{"resourceId": "folder", "name": "Folder", "resourceType": 1, "sort": "cursor-1"}}})
			} else if cursor == "cursor-1" {
				reply(w, 10200, map[string]interface{}{"hasNext": false, "data": []map[string]interface{}{{"resourceId": "file", "name": "File", "resourceType": 2, "size": "123", "sort": "cursor-2"}}})
			} else {
				t.Error("wrong cursor")
				w.WriteHeader(400)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	d.state = clientState{Binding: d.binding(), Token: "expired", UserID: "1", OpenID: "open"}
	root := &model.Object{ID: "root", Path: "/root", IsFolder: true}
	objects, err := d.List(context.Background(), root, model.ListArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || !objects[0].IsDir() || objects[1].GetSize() != 123 || objects[1].GetPath() != "/root/File" {
		t.Fatalf("wrong objects: %+v", objects)
	}
	if logins.Load() != 1 || pages.Load() != 3 {
		t.Fatal("refresh/paging requests")
	}
}
func TestNoInfiniteRefreshOrAutomaticSMS(t *testing.T) {
	var calls atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/loginServer/login" {
			reply(w, 10807, map[string]string{"temp_token": "challenge"})
		} else {
			reply(w, 10404, nil)
		}
	})
	d.state = clientState{Binding: d.binding(), Token: "expired", OpenID: "open", UserID: "1"}
	_, err := d.List(context.Background(), &model.Object{ID: "root"}, model.ListArgs{})
	if err == nil || calls.Load() != 2 {
		t.Fatalf("unexpected refresh sequence: %v/%d", err, calls.Load())
	}
}
func TestDownloadUsesNativeEndpointWithoutLeakingTokens(t *testing.T) {
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/cfi/fs/download/file/url" || r.Form.Get("file_ids") != "target" || r.Form.Get("access_token") != "secret" {
			t.Error("wrong download request")
		}
		reply(w, 10200, []map[string]string{{"fileId": "other", "url": "https://example.com/wrong"}, {"fileId": "target", "url": "https://example.com/file?signature=abc"}})
	})
	d.state = clientState{Token: "secret", OpenID: "open", UserID: "1"}
	link, err := d.Link(context.Background(), &model.Object{ID: "target"}, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "https://example.com/file?signature=abc" {
		t.Fatal("wrong file URL")
	}
	if strings.Contains(fmt.Sprint(link), "secret") {
		t.Fatal("API token leaked to download host")
	}
}
func TestInvalidDownloadResponses(t *testing.T) {
	for _, raw := range []string{`null`, `{"url":"javascript:alert(1)"}`, `[{"fileId":"other","url":"https://example.com/wrong"}]`, `[{"url":"https://a.example/file"},{"url":"https://b.example/file"}]`} {
		if _, err := parseClientDownload(json.RawMessage(raw), "target"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestHTTPFailuresAreNotReportedAsLoginSuccess(t *testing.T) {
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("password=do-not-log"))
	})
	err := d.Init(context.Background())
	if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("unexpected %v", err)
	}
}
func TestSigningUsesUnescapedValuesAndStableOrder(t *testing.T) {
	fields := url.Values{"timestamp": {"1700000000000"}, "app_id": {clientAppID}, "passport": {"u+test@example.com"}, "password": {"p&=x"}, "platform": {"device;Windows 10(64);pc"}, "org_channel": {"pc"}}
	expected := md5Text("app_id=" + clientAppID + "&org_channel=pc&passport=u+test@example.com&password=p&=x&platform=device;Windows 10(64);pc&timestamp=1700000000000" + clientSigningKey)
	if got := signClientForm(fields); got != expected {
		t.Fatalf("signature %s != %s", got, expected)
	}
	fields.Set("sign", "already-signed")
	if signClientForm(fields) != expected {
		t.Fatal("sign itself included in signature")
	}
}

// A transport failure can mean the server already sent the SMS. Never repeat it
// automatically, even across a freshly constructed driver after a NAS restart.
func TestInitialSMSFailureIsNotRetriedOnReload(t *testing.T) {
	var login, sms, verify atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loginServer/login":
			login.Add(1)
			reply(w, 10807, map[string]string{"temp_token": "challenge", "phone": "13800000000"})
		case "/loginServer/getMobileAuthCode":
			sms.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		case "/loginServer/validatePhone":
			verify.Add(1)
			t.Error("new driver must not submit a verification code")
		}
	})
	// Even a premature verify action cannot demand a code before initial login.
	d.LoginAction = "verify_sms"
	if err := d.Init(context.Background()); err == nil {
		t.Fatal("expected SMS transport failure")
	}
	d = cloneClient(d)
	_ = d.Init(context.Background())
	if login.Load() != 1 || sms.Load() != 1 || verify.Load() != 0 {
		t.Fatal("SMS repeated or verification attempted before code entry")
	}
}

func TestSMSCodeIsOptionalInDriverForm(t *testing.T) {
	for _, item := range op.GetDriverInfoMap()["BitQiu"].Additional {
		if item.Name == "sms_code" {
			if item.Required || item.Default != "" {
				t.Fatal("new driver form requires a verification code")
			}
			return
		}
	}
	t.Fatal("sms_code field missing")
}

func TestBrowseNestedFoldersAndDownload(t *testing.T) {
	var lists, downloads atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/cfi/fs/dir/resources":
			lists.Add(1)
			switch r.Form.Get("parent_id") {
			case "root":
				reply(w, 10200, map[string]interface{}{"hasNext": false, "data": []map[string]interface{}{{"resourceId": "folder+/&=目录", "resourceType": 1, "name": "Folder"}}})
			case "folder+/&=目录":
				reply(w, 10200, map[string]interface{}{"hasNext": false, "data": []map[string]interface{}{{"dirId": "9007199254740993", "resourceType": 1, "name": "Nested"}}})
			case "9007199254740993":
				reply(w, 10200, map[string]interface{}{"hasNext": false, "data": []map[string]interface{}{{"fileId": "file", "resourceType": 2, "name": "File", "size": 123}}})
			default:
				t.Errorf("wrong nested directory ID %q", r.Form.Get("parent_id"))
				w.WriteHeader(423)
			}
		case "/cfi/fs/download/file/url":
			downloads.Add(1)
			if r.Form.Get("file_ids") != "file" {
				t.Error("wrong nested download ID")
			}
			reply(w, 10200, map[string]string{"fileId": "file", "url": "https://example.com/file"})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	d.state = clientState{Token: "secret", OpenID: "open", UserID: "1"}
	var dir model.Obj = &model.Object{ID: "root", Path: "/root", IsFolder: true}
	for level := 0; level < 3; level++ {
		objs, err := d.List(context.Background(), dir, model.ListArgs{})
		if err != nil || len(objs) != 1 {
			t.Fatalf("level %d: %v", level, err)
		}
		dir = objs[0]
	}
	link, err := d.Link(context.Background(), dir, model.LinkArgs{})
	if err != nil || link.URL != "https://example.com/file" || dir.GetPath() != "/root/Folder/Nested/File" || lists.Load() != 3 || downloads.Load() != 1 {
		t.Fatalf("nested navigation failed: %v", err)
	}
}

func TestHTTP423DiagnosticsDoNotLeakSecretsOrRetry(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body, code, kind string }{
		{"json", "application/json", `{"code":10807,"message":"secret-token 13800000000","data":{"token":"secret-token"}}`, "10807", "JSON"},
		{"html", "text/html", `<html>secret-token 13800000000</html>`, "none", "HTML"},
		{"malicious_code", "application/json", `{"code":"secret-token","message":"secret-token"}`, "none", "JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(423)
				_, _ = w.Write([]byte(tc.body))
			})
			d.state = clientState{Token: "secret-token", OpenID: "open", UserID: "1"}
			_, err := d.List(context.Background(), &model.Object{ID: "root"}, model.ListArgs{})
			if err == nil {
				t.Fatal("423 treated as success")
			}
			msg := err.Error()
			if !strings.Contains(msg, "HTTP 423") || !strings.Contains(msg, "endpoint=/cfi/fs/dir/resources") || !strings.Contains(msg, "code="+tc.code) || !strings.Contains(msg, "response="+tc.kind) {
				t.Fatalf("missing diagnostics: %s", msg)
			}
			if strings.Contains(msg, "secret-token") || strings.Contains(msg, "13800000000") || calls.Load() != 1 || d.state.Token != "secret-token" {
				t.Fatal("error leaked secrets, retried or discarded a valid session without evidence")
			}
		})
	}
}

func TestHTTPAuthCodeRefreshIsBounded(t *testing.T) {
	for _, status := range []int{401, 403, 423} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var lists, logins, sms atomic.Int32
			d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cfi/fs/dir/resources":
					lists.Add(1)
					w.WriteHeader(status)
					reply(w, 10404, nil)
				case "/loginServer/login":
					logins.Add(1)
					reply(w, 10200, sessionData())
				case "/loginServer/getMobileAuthCode":
					sms.Add(1)
					t.Error("read request sent SMS")
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			d.state = clientState{Token: "old", OpenID: "open", UserID: "1"}
			_, err := d.List(context.Background(), &model.Object{ID: "root"}, model.ListArgs{})
			if err == nil || lists.Load() != 2 || logins.Load() != 1 || sms.Load() != 0 {
				t.Fatalf("unbounded auth refresh: %v", err)
			}
		})
	}
}

func TestClientObjectPrefersTypeSpecificID(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want, source string
		folder                  bool
	}{
		{"folder", `{"name":"Folder","resourceType":1,"resourceId":"resource-reference","fileId":"file-reference","dirId":"directory-id"}`, "directory-id", "dirId", true},
		{"folder_container", `{"name":"Folder","resourceId":"resource-reference","dir_id":"directory-id"}`, "directory-id", "dir_id", true},
		{"file", `{"name":"File","resourceType":2,"resourceId":"resource-reference","dirId":"parent-directory","fileId":"file-id"}`, "file-id", "fileId", false},
		{"generic_folder", `{"name":"Folder","resourceType":1,"resourceId":"directory-id"}`, "directory-id", "resourceId", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj, err := clientObject(json.RawMessage(tc.raw), "root", "/root", tc.folder)
			if err != nil {
				t.Fatal(err)
			}
			if obj.GetID() != tc.want || obj.IsDir() != tc.folder || obj.(*Object).ClientIDSource != tc.source {
				t.Fatal("wrong type-specific resource ID")
			}
		})
	}
}

func TestListingHTTP423ReportsSecondPageWithoutLeakingCursor(t *testing.T) {
	var calls atomic.Int32
	const cursor = "private-cursor+&/%=目录"
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.URL.Path != diagnosticEndpoint {
			t.Errorf("unexpected operation %s", r.URL.Path)
		}
		if r.Form.Get("sort") == "" {
			reply(w, 10200, map[string]interface{}{"hasNext": true, "data": []map[string]interface{}{{"resourceId": "file", "name": "private-name", "resourceType": 2, "sort": cursor}}})
		} else {
			if r.Form.Get("sort") != cursor {
				t.Error("pagination cursor changed")
			}
			w.WriteHeader(423)
		}
	})
	d.state = clientState{Token: "private-token", OpenID: "open", UserID: "1"}
	files, err := d.List(context.Background(), &model.Object{ID: "child"}, model.ListArgs{})
	if err == nil || len(files) != 0 {
		t.Fatal("expected second-page failure without a partial directory")
	}
	msg := err.Error()
	if !strings.Contains(msg, "page=2,cursor_present=true") || !strings.Contains(msg, "HTTP 423") || !strings.Contains(msg, "bytes=0") || calls.Load() != 4 {
		t.Fatalf("missing failure-page context: %s", msg)
	}
	for _, secret := range []string{cursor, "private-name", "private-token"} {
		if strings.Contains(msg, secret) {
			t.Fatal("pagination error leaked private values")
		}
	}
}

func TestClientPageNextMarkerTypes(t *testing.T) {
	for _, marker := range []string{"true", "1", `"1"`, `"true"`, `" true "`} {
		raw := json.RawMessage(`{"data":[{"resourceId":"file","name":"file","sort":"next"}],"hasNext":` + marker + `}`)
		objs, more, err := parseClientPage(raw, "root", "/", 24)
		if err != nil || !more || len(objs) != 1 {
			t.Fatalf("marker %s: count=%d more=%v err=%v", marker, len(objs), more, err)
		}
	}
	for _, marker := range []string{"false", "0", `"0"`, `"false"`} {
		_, more, err := parseClientPage(json.RawMessage(`{"data":[],"has_next":`+marker+`}`), "root", "/", 24)
		if err != nil || more {
			t.Fatalf("marker %s: more=%v err=%v", marker, more, err)
		}
	}
	if _, _, err := parseClientPage(json.RawMessage(`{"data":[],"hasNext":"unknown"}`), "root", "/", 24); err == nil {
		t.Fatal("invalid marker silently truncated listing")
	}
	// Some servers return more records than the requested limit.
	_, more, err := parseClientPage(json.RawMessage(` [{"resourceId":"a","name":"a"},{"resourceId":"b","name":"b"}] `), "root", "/", 1)
	if err != nil || !more {
		t.Fatal("oversized page truncated listing", err)
	}
}

func TestClientPageNullMarkerAndInvalidNames(t *testing.T) {
	_, more, err := parseClientPage(json.RawMessage(`{"data":[{"resourceId":"a","name":"a","sort":"next"}],"hasNext":null}`), "root", "/", 1)
	if err != nil || !more {
		t.Fatal("null marker truncated listing", err)
	}
	for _, name := range []string{"../outside", "a/b", "a\\b", ".."} {
		raw, _ := json.Marshal(map[string]string{"resourceId": "file", "name": name})
		if _, err := clientObject(raw, "root", "/", false); err == nil {
			t.Fatalf("accepted unsafe provider name %q", name)
		}
	}
}
