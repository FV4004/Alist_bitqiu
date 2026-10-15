package bitqiu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
)

func TestThirteenthPage423RecoversWithoutMissingOrDuplicatingFiles(t *testing.T) {
	var mu sync.Mutex
	calls := map[int]int{}
	var waits []time.Duration
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != diagnosticEndpoint {
			t.Errorf("pagination retry performed login or another operation: %s", r.URL.Path)
		}
		_ = r.ParseForm()
		page := 1
		if cursor := r.Form.Get("sort"); cursor != "" {
			var previous int
			if _, err := fmt.Sscanf(cursor, "private-cursor:%d+&/", &previous); err != nil {
				t.Error("opaque cursor changed")
			}
			page = previous + 1
		}
		mu.Lock()
		calls[page]++
		attempt := calls[page]
		mu.Unlock()
		if page == 13 && attempt == 1 {
			w.WriteHeader(423)
			return
		}
		if page > 14 {
			t.Error("read beyond final page")
		}
		count := 24
		if page == 14 {
			count = 5
		}
		records := make([]map[string]interface{}, 0, count)
		for i := 0; i < count; i++ {
			records = append(records, map[string]interface{}{"resourceId": fmt.Sprintf("%d-%d", page, i), "name": fmt.Sprintf("File %d-%d", page, i), "resourceType": 2, "sort": fmt.Sprintf("private-cursor:%d+&/", page)})
		}
		reply(w, 10200, map[string]interface{}{"hasNext": page < 14, "data": records})
	})
	d.pageDelayWait = func(ctx context.Context, delay time.Duration) error { waits = append(waits, delay); return ctx.Err() }
	d.state = clientState{Binding: d.binding(), Token: "private-token", OpenID: "open", UserID: "1"}
	d.saveClient()
	before := d.ClientState
	files, err := d.List(context.Background(), &model.Object{ID: "child"}, model.ListArgs{})
	if err != nil || len(files) != 317 {
		t.Fatalf("incomplete directory after recovery: files=%d error=%v", len(files), err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file.GetID()] {
			t.Fatal("retry duplicated a file")
		}
		seen[file.GetID()] = true
	}
	if d.ClientState != before || d.state.Token != "private-token" {
		t.Fatal("423 caused a session change")
	}
	mu.Lock()
	defer mu.Unlock()
	for page := 1; page <= 14; page++ {
		expected := 1
		if page == 13 {
			expected = 2
		}
		if calls[page] != expected {
			t.Fatalf("page %d was requested %d times", page, calls[page])
		}
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("unexpected retry delays: %v", waits)
	}
}

func TestEmpty423RetriesAreBoundedAndReturnNoPartialDirectory(t *testing.T) {
	var calls atomic.Int32
	var waits []time.Duration
	d := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(423) })
	d.pageDelayWait = func(ctx context.Context, delay time.Duration) error { waits = append(waits, delay); return ctx.Err() }
	d.state = clientState{Token: "saved-token", OpenID: "open", UserID: "1"}
	files, err := d.List(context.Background(), &model.Object{ID: "child"}, model.ListArgs{})
	if err == nil || len(files) != 0 || calls.Load() != 3 || len(waits) != 2 || waits[0] != 2*time.Second || waits[1] != 4*time.Second || !strings.Contains(err.Error(), "重试两次") || !strings.Contains(err.Error(), "page=1") {
		t.Fatalf("unbounded or incomplete retry result: calls=%d waits=%v error=%v", calls.Load(), waits, err)
	}
	if d.state.Token != "saved-token" {
		t.Fatal("blank 423 discarded the session")
	}
}

func TestPaginationRetryAfterAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		cancel       bool
		calls        int
		wait         time.Duration
	}{
		{"retry_after", "8", false, 2, 8 * time.Second},
		{"long_retry_after", "60", false, 1, 0},
		{"cancel", "", true, 1, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var waited time.Duration
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", tc.header)
					w.WriteHeader(423)
					return
				}
				reply(w, 10200, map[string]interface{}{"hasNext": false, "data": []interface{}{}})
			})
			d.state = clientState{Token: "token", OpenID: "open", UserID: "1"}
			d.pageDelayWait = func(ctx context.Context, delay time.Duration) error {
				waited = delay
				if tc.cancel {
					cancel()
				}
				return ctx.Err()
			}
			_, err := d.List(ctx, &model.Object{ID: "child"}, model.ListArgs{})
			if int(calls.Load()) != tc.calls || waited != tc.wait {
				t.Fatalf("Retry-After/cancellation ignored: calls=%d wait=%v", calls.Load(), waited)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation not propagated: %v", err)
			}
			if tc.header == "8" && err != nil {
				t.Fatal(err)
			}
			if tc.header == "60" && err == nil {
				t.Fatal("retried before the requested long delay")
			}
		})
	}
}

func TestPaginationDefaultPacingAndCancellableWait(t *testing.T) {
	d := &BitQiu{}
	if d.pageInterval() != time.Second {
		t.Fatal("existing storages must default to one second")
	}
	d.PageIntervalMS = "20"
	started := time.Now()
	d.lastPageRequest = started
	if err := d.paceClientPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.lastPageRequest.Sub(started) < 20*time.Millisecond {
		t.Fatal("consecutive page request was not paced")
	}
	d.PageIntervalMS = "1000"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := d.paceClientPage(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("page wait ignored cancellation: %v", err)
	}
}
