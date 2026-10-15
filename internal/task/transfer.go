package task

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

type TransferSnapshot struct {
	TransferredBytes    int64   `json:"transferred_bytes"`
	SpeedBytesPerSecond float64 `json:"speed_bytes_per_second"`
	CompletionReason    string  `json:"completion_reason,omitempty"`
}

type transferSample struct {
	at    time.Time
	bytes int64
}

// TransferMeter counts real stream reads. Only the byte count and terminal
// reason are persisted; a restored task must collect fresh speed samples.
type TransferMeter struct {
	mu       sync.Mutex
	bytes    int64
	running  bool
	canceled bool
	reason   string
	lastRead time.Time
	samples  []transferSample
}

func CompletionReason(err error) string {
	if err == nil {
		return "completed"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "failed"
}
func (m *TransferMeter) Start(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes = 0
	m.running = true
	m.canceled = false
	m.reason = ""
	m.lastRead = time.Time{}
	m.samples = []transferSample{{at: now}}
}
func (m *TransferMeter) Add(n int, now time.Time) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes += int64(n)
	m.lastRead = now
	if len(m.samples) == 0 {
		m.samples = []transferSample{{at: now, bytes: m.bytes}}
		return
	}
	if now.Sub(m.samples[len(m.samples)-1].at) >= 500*time.Millisecond {
		m.samples = append(m.samples, transferSample{at: now, bytes: m.bytes})
	}
	// Retain one baseline before the five-second window.
	for len(m.samples) > 1 && now.Sub(m.samples[1].at) > 5*time.Second {
		m.samples = m.samples[1:]
	}
}
func (m *TransferMeter) CancelRequested() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canceled = true
	m.reason = "user_canceled"
}
func (m *TransferMeter) Stop(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	if m.canceled {
		m.reason = "user_canceled"
	} else {
		m.reason = CompletionReason(err)
	}
}
func (m *TransferMeter) Snapshot(now time.Time) TransferSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := TransferSnapshot{TransferredBytes: m.bytes, CompletionReason: m.reason}
	if m.running && !m.lastRead.IsZero() && now.Sub(m.lastRead) < 3*time.Second && len(m.samples) > 0 {
		first := m.samples[0]
		if elapsed := now.Sub(first.at).Seconds(); elapsed > 0 {
			s.SpeedBytesPerSecond = float64(m.bytes-first.bytes) / elapsed
		}
	}
	return s
}
func (m *TransferMeter) MarshalJSON() ([]byte, error) {
	s := m.Snapshot(time.Now())
	s.SpeedBytesPerSecond = 0
	return json.Marshal(s)
}
func (m *TransferMeter) UnmarshalJSON(data []byte) error {
	var s TransferSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes = s.TransferredBytes
	m.reason = s.CompletionReason
	m.running = false
	m.samples = nil
	m.lastRead = time.Time{}
	m.canceled = s.CompletionReason == "user_canceled"
	return nil
}
