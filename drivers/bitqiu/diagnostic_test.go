package bitqiu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alist-org/alist/v3/internal/model"
)

func TestInvalidParametersDoNotClearSessionOrLogin(t *testing.T) {
	for _, status := range []int{200, 423} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != diagnosticEndpoint {
					t.Errorf("parameter error triggered login: %s", r.URL.Path)
				}
				w.WriteHeader(status)
				reply(w, 10401, nil)
			})
			d.state = clientState{Token: "saved-token", OpenID: "open", UserID: "1"}
			_, err := d.List(context.Background(), &model.Object{ID: "root"}, model.ListArgs{})
			if err == nil || calls.Load() != 1 || d.state.Token != "saved-token" {
				t.Fatalf("parameter error altered the session or retried: %v", err)
			}
		})
	}
}

func TestDiagnosticComparesSameSessionWithoutLoginOrLeakingSecrets(t *testing.T) {
	if _, err := exec.LookPath("wget"); err != nil {
		t.Skip("wget not installed")
	}
	t.Setenv("no_proxy", "127.0.0.1,localhost") // local test server only
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	var mu sync.Mutex
	var forms []url.Values
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != diagnosticEndpoint {
			t.Errorf("diagnostic performed another operation: %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.UserAgent() != clientUA || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("clients used different headers")
		}
		mu.Lock()
		forms = append(forms, r.PostForm)
		mu.Unlock()
		reply(w, 10401, map[string]string{"token": "secret-token", "phone": "13800000000"})
	})
	d.Password = "private-password"
	d.state = clientState{Binding: d.binding(), Token: "secret-token", OpenID: "private-open-id", UserID: "private-user-id"}
	d.RootFolderID = "private-root-id"
	d.saveClient()
	before := d.ClientState
	d.LoginAction = "diagnose_http"
	d.SMSCode = "123456"
	err := d.Init(context.Background())
	if err == nil {
		t.Fatal("expected diagnostic summary")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Go[HTTP=200,code=10401]") || !strings.Contains(msg, "wget[HTTP=200,code=10401,response=JSON") {
		t.Fatalf("unexpected diagnostic summary: %s", msg)
	}
	for _, secret := range []string{"secret-token", "13800000000", "private-password", "private-open-id", "private-user-id", "private-root-id", "123456"} {
		if strings.Contains(msg, secret) {
			t.Fatal("diagnostic leaked a secret")
		}
	}
	if d.LoginAction != "none" || d.SMSCode != "" || d.ClientState != before {
		t.Fatal("diagnostic changed session or left an action/code behind")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forms) != 2 || forms[0].Encode() != forms[1].Encode() {
		t.Fatal("diagnostic requests did not use identical fields")
	}
	if forms[0].Get("access_token") != "secret-token" || forms[0].Get("platform") != "stable-device" || forms[0].Get("parent_id") != "private-root-id" {
		t.Fatal("diagnostic did not use saved credentials and root ID")
	}
	files, err := os.ReadDir(temp)
	if err != nil || len(files) != 0 {
		t.Fatal("temporary credential files retained")
	}
}

func TestDiagnosticWithoutSessionDoesNotSendAnyRequests(t *testing.T) {
	var calls atomic.Int32
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	d.LoginAction = "diagnose_http"
	err := d.Init(context.Background())
	if err == nil || calls.Load() != 0 || d.LoginAction != "none" {
		t.Fatal("diagnostic without session performed a login or request")
	}
}
