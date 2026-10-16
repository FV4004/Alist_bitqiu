package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/alist-org/alist/v3/internal/db"
	"github.com/alist-org/alist/v3/internal/model"
	"gorm.io/gorm"
)

const NASMaxConcurrency = 5
const nasConcurrencyKey = "bitqiu_nas_concurrency_v1"

// Admission is separate from the shared copy queue's worker count. Only NAS
// executions consume these slots; ordinary copies retain their original rules.
var nasLimit = NASMaxConcurrency // protected by nasRegistry
var nasDispatchMu sync.Mutex
var nasLimitUpdateMu sync.Mutex
var nasQueueSequence atomic.Uint64
var nasStartSequence atomic.Uint64

type NASConcurrency struct {
	Limit   int    `json:"limit"`
	Maximum int    `json:"maximum"`
	Running int    `json:"running"`
	Paused  int    `json:"paused"`
	Pending int    `json:"pending"`
	Warning string `json:"warning,omitempty"`
}

func NASConcurrencyStatus() NASConcurrency {
	nasRegistry.Lock()
	defer nasRegistry.Unlock()
	v := NASConcurrency{Limit: nasLimit, Maximum: NASMaxConcurrency}
	for _, n := range nasRegistry.tasks {
		n.mu.Lock()
		if n.active {
			if n.record.State == "downloading" {
				v.Running++
			} else {
				v.Pending++
			}
		}
		n.mu.Unlock()
	}
	return v
}
func loadNASConcurrency() (int, error) {
	var item model.TaskItem
	err := db.GetDb().Where("key = ?", nasConcurrencyKey).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NASMaxConcurrency, nil
	}
	if err != nil {
		return 0, err
	}
	var v struct {
		Limit int `json:"limit"`
	}
	if err = json.Unmarshal([]byte(item.PersistData), &v); err != nil {
		return 0, err
	}
	if v.Limit < 1 || v.Limit > NASMaxConcurrency {
		return 0, errors.New("NAS 并发配置无效，必须为 1 至 5")
	}
	return v.Limit, nil
}
func saveNASConcurrency(limit int) error {
	data, _ := json.Marshal(struct {
		Limit int `json:"limit"`
	}{limit})
	var item model.TaskItem
	err := db.GetDb().Where("key = ?", nasConcurrencyKey).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.GetDb().Create(&model.TaskItem{Key: nasConcurrencyKey, PersistData: string(data)}).Error
	}
	if err != nil {
		return err
	}
	return db.GetDb().Model(&model.TaskItem{}).Where("key = ?", nasConcurrencyKey).Update("persist_data", string(data)).Error
}

// Reserve a bounded number of entries, then hand them to the original copy
// manager. No worker waits for a NAS slot and no alternative worker pool exists.
func dispatchNAS() {
	nasDispatchMu.Lock()
	defer nasDispatchMu.Unlock()
	if CopyTaskManager == nil {
		return
	}
	nasRegistry.Lock()
	slots := nasLimit
	type candidate struct {
		n     *nasTask
		order uint64
	}
	candidates := []candidate{}
	for _, n := range nasRegistry.tasks {
		n.mu.Lock()
		if n.active || n.queued && n.record.State == "waiting" {
			slots--
		}
		if !n.active && !n.queued && n.ready && n.record.State == "waiting" && n.creator != nil {
			candidates = append(candidates, candidate{n, n.queueOrder})
		}
		n.mu.Unlock()
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].order < candidates[j].order })
	if slots < 0 {
		slots = 0
	}
	admitted := []*nasTask{}
	for _, c := range candidates {
		if slots == 0 {
			break
		}
		n := c.n
		n.mu.Lock()
		if !n.active && !n.queued && n.ready && n.record.State == "waiting" && n.creator != nil {
			n.queued = true
			slots--
			admitted = append(admitted, n)
		}
		n.mu.Unlock()
	}
	nasRegistry.Unlock()
	for _, n := range admitted {
		enqueueNAS(n)
	}
}

// Returns a conservative count: even a transfer that is stopping occupies its
// slot until its source reader and destination writer have actually closed.
func activeNASLocked() int {
	count := 0
	for _, n := range nasRegistry.tasks {
		n.mu.Lock()
		if n.active {
			count++
		}
		n.mu.Unlock()
	}
	return count
}
func SetNASConcurrency(ctx context.Context, limit int) (NASConcurrency, error) {
	if limit < 1 || limit > NASMaxConcurrency {
		return NASConcurrency{}, errors.New("同时下载任务数必须为 1 至 5")
	}
	if err := NASDownloadsAvailable(); err != nil {
		return NASConcurrency{}, err
	}
	nasLimitUpdateMu.Lock()
	defer nasLimitUpdateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return NASConcurrency{}, err
	}
	// A failed settings write must not change the live limit or pause any task.
	if err := saveNASConcurrency(limit); err != nil {
		return NASConcurrency{}, err
	}
	type stop struct {
		n      *nasTask
		order  uint64
		cancel context.CancelFunc
		done   <-chan struct{}
	}
	stops := []stop{}
	nasRegistry.Lock()
	nasLimit = limit
	for _, n := range nasRegistry.tasks {
		n.mu.Lock()
		if n.active && n.record.State == "downloading" {
			stops = append(stops, stop{n: n, order: n.startOrder, cancel: n.cancel, done: n.done})
		}
		n.mu.Unlock()
	}
	sort.Slice(stops, func(i, j int) bool { return stops[i].order > stops[j].order })
	excess := len(stops) - limit
	if excess < 0 {
		excess = 0
	}
	stops = stops[:excess]
	for _, s := range stops {
		s.n.mu.Lock()
		if s.n.active && s.n.record.State == "downloading" {
			s.n.record.State = "pausing"
			s.n.record.Error = fmt.Sprintf("并发上限调整为 %d，已按最晚开始优先暂停；可手动开始继续", limit)
		}
		s.n.mu.Unlock()
	}
	nasRegistry.Unlock()
	for _, s := range stops {
		if s.cancel != nil {
			s.cancel()
		}
	}
	defer dispatchNAS()
	paused := 0
	for _, s := range stops {
		select {
		case <-s.done:
			s.n.mu.Lock()
			if s.n.record.State == "paused" {
				paused++
			}
			s.n.mu.Unlock()
		case <-ctx.Done():
			v := NASConcurrencyStatus()
			v.Paused = paused
			v.Warning = "并发上限已保存，部分任务仍在暂停中；不会启动新任务占用名额"
			return v, nil
		}
	}
	v := NASConcurrencyStatus()
	v.Paused = paused
	if err := SaveNASDownloads(); err != nil {
		v.Warning = "并发上限已保存，但任务状态保存失败，请检查数据库写入状态"
	}
	return v, nil
}
