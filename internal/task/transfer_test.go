package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func TestTransferMeter(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var m TransferMeter
	m.Start(start)
	m.Add(1024, start.Add(time.Second))
	m.Add(1024, start.Add(2*time.Second))
	s := m.Snapshot(start.Add(2 * time.Second))
	if s.TransferredBytes != 2048 || s.SpeedBytesPerSecond != 1024 {
		t.Fatalf("wrong stream rate: %+v", s)
	}
	if m.Snapshot(start.Add(6*time.Second)).SpeedBytesPerSecond != 0 {
		t.Fatal("stalled transfer retained a stale speed")
	}
	m.Stop(context.DeadlineExceeded)
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	var restored TransferMeter
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	s = restored.Snapshot(start.Add(8 * time.Second))
	if s.CompletionReason != "timeout" || s.TransferredBytes != 2048 || s.SpeedBytesPerSecond != 0 {
		t.Fatalf("lost persisted outcome: %+v", s)
	}
	restored.Start(start.Add(10 * time.Second))
	s = restored.Snapshot(start.Add(11 * time.Second))
	if s.TransferredBytes != 0 || s.CompletionReason != "" {
		t.Fatalf("retry retained old measurements: %+v", s)
	}
	restored.CancelRequested()
	restored.Stop(context.Canceled)
	if restored.Snapshot(start).CompletionReason != "user_canceled" {
		t.Fatal("user cancellation not recorded")
	}
}
func TestCompletionReason(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, "completed"}, {context.Canceled, "canceled"}, {fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout"},
		{&url.Error{Op: "GET", URL: "https://fixture.invalid", Err: os.ErrDeadlineExceeded}, "timeout"}, {errors.New("disk full"), "failed"},
	} {
		if got := CompletionReason(c.err); got != c.want {
			t.Fatalf("%v: %s != %s", c.err, got, c.want)
		}
	}
}
func TestTransferMeterConcurrentSnapshots(t *testing.T) {
	var m TransferMeter
	m.Start(time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				m.Add(10, time.Now())
				_ = m.Snapshot(time.Now())
				if _, err := json.Marshal(&m); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if m.Snapshot(time.Now()).TransferredBytes != 4000 {
		t.Fatal("lost concurrent byte counts")
	}
}
