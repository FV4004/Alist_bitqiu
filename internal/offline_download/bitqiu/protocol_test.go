package bitqiu

import (
	"fmt"
	"strings"
	"testing"
)

// Fixtures model fields read by the official frontend; they are synthetic,
// not live responses and not evidence of the creation endpoint's contract.
func TestCloudTaskUsesTaskIDWithoutPrecisionLoss(t *testing.T) {
	page, err := decodeCloudTaskPage([]byte(`{"data":[{"id":"ui-key","taskId":9007199254740993,"dirId":"result-dir","status":1,"progress":12.5}],"totalCount":1,"totalPageCount":1}`))
	if err != nil {
		t.Fatal(err)
	}
	task := page.Tasks[0]
	id, err := task.remoteIdentity()
	if err != nil || id != "9007199254740993" || string(task.ID) != `"ui-key"` || string(task.DirID) != `"result-dir"` {
		t.Fatal("wire identities were lost or conflated")
	}
	if task.state() != "downloading" || task.progressText() != "12.5" {
		t.Fatal("state or progress mismatch")
	}
}

func TestCloudTaskUnknownAndPartialNeverBecomeSuccess(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"0", "waiting"}, {"1", "downloading"}, {"2", "paused"},
		{"3", "failed"}, {"4", "completed"}, {"5", "partial"},
		{"99", "unknown"}, {"null", "unknown"},
	} {
		page, err := decodeCloudTaskPage([]byte(fmt.Sprintf(`{"data":[{"taskId":"remote","status":%s,"progress":100}]}`, tc.status)))
		if err != nil || page.Tasks[0].state() != tc.want {
			t.Fatalf("status %s: %v", tc.status, err)
		}
	}
	for _, tc := range []struct{ field, want string }{
		{"", ""}, {`,"progress":null`, ""}, {`,"progress":-1`, "0"}, {`,"progress":101`, "100"},
	} {
		page, err := decodeCloudTaskPage([]byte(`{"data":[{"taskId":"remote"` + tc.field + `}]}`))
		if err != nil || page.Tasks[0].state() != "unknown" || page.Tasks[0].progressText() != tc.want {
			t.Fatal("missing status/progress or display clamp mishandled")
		}
	}
}

func TestCloudTaskMalformedResponseCannotEraseRemoteIdentity(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"data":null}`, `{"data":{}}`, `{"data":[null]}`,
		`{"data":[{"id":"ui-only"}]}`,
		`{"data":[{"taskId":1.5}]}`, `{"data":[{"taskId":1e3}]}`,
		`{"data":[{"taskId":true}]}`, `{"data":[{"taskId":{}}]}`,
		`{"data":[{"taskId":""}]}`, `{"data":[{"taskId":"remote\nsecret"}]}`,
		`{"data":[{"taskId":"remote","status":"4"}]}`,
		`{"data":[{"taskId":"remote","progress":"100"}]}`,
	} {
		page, err := decodeCloudTaskPage([]byte(body))
		if err == nil || page.Tasks != nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "ui-only") {
			t.Fatal("malformed page accepted or response content leaked")
		}
	}
	page, err := decodeCloudTaskPage([]byte(`{"data":[]}`))
	if err != nil || page.Tasks == nil || len(page.Tasks) != 0 {
		t.Fatal("explicit empty page rejected")
	}
}
