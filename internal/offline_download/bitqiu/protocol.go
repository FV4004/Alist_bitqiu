package bitqiu

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// These fields are consumed by the official PC frontend's modules 1611/1614.
// Identity fields retain their wire representation. In particular id is a Vue
// list key, whereas taskId is used for remote operations; they are not aliases.
// No creation-response decoder is provided without evidence of its contract.
type cloudTask struct {
	ID          json.RawMessage `json:"id"`
	TaskID      json.RawMessage `json:"taskId"`
	DirID       json.RawMessage `json:"dirId"`
	TaskName    string          `json:"taskName"`
	DownloadURL string          `json:"downloadUrl"`
	Status      *int            `json:"status"`
	Progress    *float64        `json:"progress"`
	ErrorCode   json.RawMessage `json:"errorCode"`
}

type cloudTaskPage struct {
	Tasks          []cloudTask     `json:"data"`
	TotalCount     json.RawMessage `json:"totalCount"`
	TotalPageCount json.RawMessage `json:"totalPageCount"`
	ActiveCount    json.RawMessage `json:"activeCount"`
}

func decodeCloudTaskPage(data []byte) (cloudTaskPage, error) {
	var page cloudTaskPage
	// Missing/null data is not an empty task list: it may be a changed or
	// malformed response. Never treat it as evidence that a task disappeared.
	if json.Unmarshal(data, &page) != nil || page.Tasks == nil {
		return cloudTaskPage{}, errors.New("BitQiu 云任务列表格式无效")
	}
	for _, task := range page.Tasks {
		if _, err := task.remoteIdentity(); err != nil {
			return cloudTaskPage{}, err
		}
	}
	return page, nil
}

func (t cloudTask) remoteIdentity() (string, error) {
	raw := bytes.TrimSpace(t.TaskID)
	var id string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &id) != nil {
			id = ""
		}
	} else {
		// Preserve integer IDs beyond float64 precision. Other scalar types,
		// exponents and fractional values are not usable durable identities.
		id = string(raw)
		for _, c := range id {
			if c < '0' || c > '9' {
				id = ""
				break
			}
		}
	}
	if id == "" || id == "0" || !validRemoteIdentity(id) {
		return "", errors.New("BitQiu 云任务缺少有效 taskId；不能使用 id 替代")
	}
	return id, nil
}

func validRemoteIdentity(id string) bool {
	if len(id) > 1024 {
		return false
	}
	for _, c := range id {
		if c <= ' ' || c == '\x7f' {
			return false
		}
	}
	return true
}

func (t cloudTask) state() string {
	if t.Status != nil {
		switch *t.Status {
		case 0:
			return "waiting"
		case 1:
			return "downloading"
		case 2:
			return "paused"
		case 3:
			return "failed"
		case 4:
			return "completed"
		case 5:
			return "partial"
		}
	}
	return "unknown"
}

// progressText follows the frontend's display clamp, without using progress to
// infer completion. Missing progress remains unknown instead of becoming zero.
func (t cloudTask) progressText() string {
	if t.Progress == nil {
		return ""
	}
	p := *t.Progress
	if p < 0 {
		p = 0
	} else if p > 100 {
		p = 100
	}
	return strconv.FormatFloat(p, 'f', -1, 64)
}
