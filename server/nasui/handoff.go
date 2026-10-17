package nasui

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

type handoff struct {
	Token    string `json:"token"`
	ClientID string `json:"client_id"`
	expires  time.Time
}
type handoffStore struct {
	sync.Mutex
	entries map[string]handoff
}

var nasHandoffs = handoffStore{entries: make(map[string]handoff)}

func (s *handoffStore) issue(token, clientID string, now time.Time) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	s.Lock()
	defer s.Unlock()
	for key, value := range s.entries {
		if !value.expires.After(now) {
			delete(s.entries, key)
		}
	}
	// Bound memory even if an authenticated client repeatedly requests tickets.
	if len(s.entries) >= 1024 {
		return "", errHandoffFull{}
	}
	key := hex.EncodeToString(raw[:])
	s.entries[key] = handoff{token, clientID, now.Add(time.Minute)}
	return key, nil
}

type errHandoffFull struct{}

func (errHandoffFull) Error() string {
	return "Too many pending NAS login handoffs; retry in one minute"
}
func (s *handoffStore) take(key string, now time.Time) (handoff, bool) {
	s.Lock()
	defer s.Unlock()
	value, ok := s.entries[key]
	delete(s.entries, key)
	return value, ok && value.expires.After(now)
}

// IssueHandoff must be registered behind Auth + AuthAdmin. No passwords or
// long-lived tokens go into the navigation URL; the ticket is single-use, 60s.
func IssueHandoff(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	user, ok := c.Get("user")
	u, isUser := user.(*model.User)
	if !ok || !isUser || !u.IsAdmin() {
		common.ErrorStrResp(c, "You are not an admin", 403)
		return
	}
	token, clientID := c.GetHeader("Authorization"), c.GetHeader("Client-Id")
	if token == "" || clientID == "" {
		common.ErrorStrResp(c, "Missing AList login or device identity", 401)
		return
	}
	key, err := nasHandoffs.issue(token, clientID, time.Now())
	if err != nil {
		common.ErrorResp(c, err, 503)
		return
	}
	common.SuccessResp(c, gin.H{"ticket": key})
}

// RedeemHandoff only exchanges a random ticket. Every subsequent NAS request
// still passes through the original token, device-session, and admin checks.
func RedeemHandoff(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req struct {
		Ticket string `json:"ticket"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Ticket) != 64 {
		common.ErrorStrResp(c, "NAS login handoff expired; reopen NAS from AList", 401)
		return
	}
	value, ok := nasHandoffs.take(req.Ticket, time.Now())
	if !ok {
		common.ErrorStrResp(c, "NAS login handoff expired; reopen NAS from AList", 401)
		return
	}
	common.SuccessResp(c, value)
}
