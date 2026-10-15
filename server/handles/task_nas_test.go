package handles

import (
	"context"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/xhofe/tache"
)

func TestNASTaskInfo(t *testing.T) {
	for _, c := range []struct {
		state tache.State
		err   error
		user  bool
		want  string
	}{
		{tache.StateSucceeded, nil, false, "completed"},
		{tache.StateCanceled, context.Canceled, true, "user_canceled"},
		{tache.StateCanceled, context.Canceled, false, "canceled"},
		{tache.StateFailed, context.DeadlineExceeded, false, "timeout"},
		{tache.StateFailed, nil, false, "failed"},
	} {
		task := &fs.CopyTask{}
		task.Transfer.Start(time.Now())
		if c.user {
			task.Transfer.CancelRequested()
		}
		task.Transfer.Stop(c.err)
		task.SetState(c.state)
		// Restored tasks may lack a Go error value; the persisted reason still wins.
		info := getTaskInfo(task)
		if info.CompletionReason != c.want || info.SpeedBytesPerSecond != 0 {
			t.Fatalf("state %v: %+v", c.state, info)
		}
	}
}
