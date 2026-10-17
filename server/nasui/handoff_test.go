package nasui

import (
	"bytes"
	"encoding/json"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNASHandoffSingleUseExpiryAndCapacity(t *testing.T) {
	s := handoffStore{entries: make(map[string]handoff)}
	now := time.Now()
	key, err := s.issue("fixture-token", "fixture-device", now)
	if err != nil || len(key) != 64 {
		t.Fatal(key, err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, ok := s.take(key, now)
			if ok {
				wins.Add(1)
				if v.Token != "fixture-token" || v.ClientID != "fixture-device" {
					t.Error("identity changed")
				}
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("ticket replay", wins.Load())
	}
	key, _ = s.issue("token", "id", now)
	if _, ok := s.take(key, now.Add(time.Minute)); ok {
		t.Fatal("accepted expired ticket")
	}
	for i := 0; i < 1024; i++ {
		if _, err = s.issue("token", "id", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.issue("token", "id", now); err == nil {
		t.Fatal("unbounded pending tickets")
	}
	if _, err = s.issue("token", "id", now.Add(time.Minute)); err != nil {
		t.Fatal("did not clear expired tickets", err)
	}
}
func TestNASHandoffHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []model.Roles{nil, {2}} {
		r := gin.New()
		r.POST("/issue", func(c *gin.Context) { c.Set("user", &model.User{Role: role}); IssueHandoff(c) })
		r.POST("/redeem", RedeemHandoff)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/issue", nil)
		req.Header.Set("Authorization", "fixture-token")
		req.Header.Set("Client-Id", "fixture-device")
		r.ServeHTTP(w, req)
		var value struct {
			Code int `json:"code"`
			Data struct {
				Ticket string `json:"ticket"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if len(role) == 0 {
			if value.Code != 403 {
				t.Fatal(w.Body.String())
			}
			continue
		}
		if value.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Body.String())
		}
		for i := 0; i < 2; i++ {
			body, _ := json.Marshal(map[string]string{"ticket": value.Data.Ticket})
			out := httptest.NewRecorder()
			r.ServeHTTP(out, httptest.NewRequest("POST", "/redeem", bytes.NewReader(body)))
			var result struct {
				Code int     `json:"code"`
				Data handoff `json:"data"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cached credentials")
			}
			if i == 0 {
				if result.Code != 200 || result.Data.Token != "fixture-token" || result.Data.ClientID != "fixture-device" {
					t.Fatal(out.Body.String())
				}
			} else if result.Code != 401 {
				t.Fatal("replayed ticket", out.Body.String())
			}
		}
	}
}
