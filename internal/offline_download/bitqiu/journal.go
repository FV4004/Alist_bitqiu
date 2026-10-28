// Package bitqiu contains the isolated BitQiu cloud-download adapter.
// Protocol registration is withheld until the official create/query contract
// has been verified. The journal prevents retries from spending remote quota twice.
package bitqiu

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"gorm.io/gorm"
)

const journalPrefix = "bitqiu_cloud_submission_v1:"

var journalMu sync.Mutex
var errScopeChanged = errors.New("BitQiu 云任务所属用户、账号或目标已改变，禁止复用")

type submissionScope struct {
	CreatorID   uint   `json:"creator_id"`
	StorageID   uint   `json:"storage_id"`
	AccountHash string `json:"account_hash"`
	ParentID    string `json:"parent_id"`
	URLHash     string `json:"url_hash"`
}
type submissionRecord struct {
	Version   int             `json:"version"`
	UID       string          `json:"uid"`
	Scope     submissionScope `json:"scope"`
	State     string          `json:"state"`
	RemoteID  string          `json:"remote_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
type submissionJournal struct{ database *gorm.DB }

func (s submissionScope) valid() bool {
	hash := func(v string) bool {
		if len(v) != 64 {
			return false
		}
		for _, r := range v {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	return s.CreatorID != 0 && s.StorageID != 0 && s.ParentID != "" && len(s.ParentID) <= 1024 && hash(s.AccountHash) && hash(s.URLHash)
}
func validSubmissionUID(uid string) bool {
	if len(uid) == 0 || len(uid) > 128 {
		return false
	}
	for _, r := range uid {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (j submissionJournal) read(uid string) (submissionRecord, bool, error) {
	var record submissionRecord
	if j.database == nil {
		return record, false, errors.New("BitQiu 云任务数据库未就绪")
	}
	if !validSubmissionUID(uid) {
		return record, false, errors.New("无效云任务标识")
	}
	var item model.TaskItem
	err := j.database.Where("key = ?", journalPrefix+uid).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, errors.New("BitQiu 云任务记录读取失败；未覆盖原记录")
	}
	if json.Unmarshal([]byte(item.PersistData), &record) != nil || record.Version != 1 || record.UID != uid || !record.Scope.valid() {
		return record, false, errors.New("BitQiu 云任务记录无效；未覆盖原记录")
	}
	if record.State != "submitting" && record.State != "uncertain" && record.State != "submitted" {
		return record, false, errors.New("BitQiu 云任务提交状态无效；未覆盖原记录")
	}
	if (record.State == "submitted") != (record.RemoteID != "") {
		return record, false, errors.New("BitQiu 云任务远端标识不一致；禁止重新提交")
	}
	return record, true, nil
}
func (j submissionJournal) write(record submissionRecord, create bool) error {
	record.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if create {
		err = j.database.Create(&model.TaskItem{Key: journalPrefix + record.UID, PersistData: string(raw)}).Error
	} else {
		err = j.database.Model(&model.TaskItem{}).Where("key = ?", journalPrefix+record.UID).Update("persist_data", string(raw)).Error
	}
	if err != nil {
		return errors.New("BitQiu 云任务提交状态保存失败；禁止自动重新提交")
	}
	return nil
}

// Reserve must succeed durably BEFORE calling the remote create endpoint.
// A reused UID never authorizes another create call, including after restart.
func (j submissionJournal) Reserve(uid string, scope submissionScope) (submissionRecord, bool, error) {
	journalMu.Lock()
	defer journalMu.Unlock()
	if !scope.valid() {
		return submissionRecord{}, false, errors.New("云任务所属用户、存储或目标信息不完整")
	}
	record, exists, err := j.read(uid)
	if err != nil {
		return record, false, err
	}
	if exists {
		if record.Scope != scope {
			return submissionRecord{}, false, errScopeChanged
		}
		return record, false, nil
	}
	record = submissionRecord{Version: 1, UID: uid, Scope: scope, State: "submitting", CreatedAt: time.Now().UTC()}
	if err = j.write(record, true); err != nil {
		return submissionRecord{}, false, err
	}
	return record, true, nil
}

// Confirm records the response task ID. An existing different ID is never replaced.
func (j submissionJournal) Confirm(uid string, scope submissionScope, remoteID string) error {
	if remoteID == "" || len(remoteID) > 1024 || strings.TrimSpace(remoteID) != remoteID || strings.ContainsAny(remoteID, "\r\n\x00") {
		return errors.New("BitQiu 未返回有效远端任务标识；需要人工确认提交结果")
	}
	journalMu.Lock()
	defer journalMu.Unlock()
	record, exists, err := j.read(uid)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("云任务提交记录不存在")
	}
	if record.Scope != scope {
		return errScopeChanged
	}
	if record.RemoteID != "" {
		if record.RemoteID != remoteID {
			return errors.New("同一本地任务返回不同远端标识；禁止覆盖")
		}
		return nil
	}
	record.RemoteID = remoteID
	record.State = "submitted"
	return j.write(record, false)
}

// Uncertain is used when a create response is lost; polling/reconciliation or
// manual review is required. It never grants permission to submit the URL again.
func (j submissionJournal) Uncertain(uid string, scope submissionScope) error {
	journalMu.Lock()
	defer journalMu.Unlock()
	record, exists, err := j.read(uid)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("云任务提交记录不存在")
	}
	if record.Scope != scope {
		return errScopeChanged
	}
	if record.RemoteID != "" {
		return nil
	}
	record.State = "uncertain"
	return j.write(record, false)
}
