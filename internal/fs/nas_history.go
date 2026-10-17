package fs

import (
	"errors"
	"sort"
	"sync"
	"time"
)

const NASHistoryDays = 90

var nasHistoryOnce sync.Once

func nasHistoryState(state string) bool { return nasTerminal(state) || state == "failed" }
func nasGroupKey(r NASRecord) string {
	if r.GroupID != "" {
		return r.GroupID
	}
	return r.ID
}

type NASHistoryResult struct {
	Deleted         int `json:"deleted"`
	Skipped         int `json:"skipped"`
	PartialFailures int `json:"partial_failures"`
}

// Selecting a grouped child removes the whole group, and only when every member
// is terminal and inactive. Locks serialize deletion with retry/control actions.
// cutoff zero means an explicit user request; automatic pruning keeps partials.
func ClearNASHistory(ids []string, all, removePartials bool, cutoff time.Time) (NASHistoryResult, error) {
	result := NASHistoryResult{}
	if err := NASDownloadsAvailable(); err != nil {
		return result, err
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	nasRegistry.Lock()
	groups := map[string][]*nasTask{}
	idsByTask := map[*nasTask]string{}
	for _, n := range nasRegistry.tasks {
		n.mu.Lock()
		key := nasGroupKey(n.record)
		groups[key] = append(groups[key], n)
		idsByTask[n] = n.record.ID
		n.mu.Unlock()
	}
	nasRegistry.Unlock()
	chosen := map[string][]*nasTask{}
	locked := []*nasTask{}
	for key, members := range groups {
		pick := all || selected[key]
		for _, n := range members {
			pick = pick || selected[idsByTask[n]]
		}
		if pick {
			chosen[key] = members
			locked = append(locked, members...)
		}
	}
	// Globally ordered action locks make overlapping bulk clears and retries safe.
	sort.Slice(locked, func(i, j int) bool { return idsByTask[locked[i]] < idsByTask[locked[j]] })
	for _, n := range locked {
		n.action.Lock()
	}
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i].action.Unlock()
		}
	}()
	removed := []*nasTask{}
	nasSaveMu.Lock()
	nasRegistry.Lock()
	for _, members := range chosen {
		valid := true
		for _, n := range members {
			n.mu.Lock()
			r := n.record
			if nasRegistry.tasks[r.ID] != n || n.active || !nasHistoryState(r.State) || (!cutoff.IsZero() && (r.EndTime == nil || !r.EndTime.Before(cutoff))) {
				valid = false
			}
			n.mu.Unlock()
		}
		if !valid {
			result.Skipped += len(members)
			continue
		}
		for _, n := range members {
			delete(nasRegistry.tasks, idsByTask[n])
			removed = append(removed, n)
		}
	}
	nasRegistry.Unlock()
	var err error
	if len(removed) > 0 {
		err = saveNASDownloadsLocked()
	}
	if err != nil {
		nasRegistry.Lock()
		for _, n := range removed {
			nasRegistry.tasks[idsByTask[n]] = n
		}
		nasRegistry.Unlock()
	}
	nasSaveMu.Unlock()
	if err != nil {
		return result, errors.New("清理记录保存失败，记录已恢复")
	}
	result.Deleted = len(removed)
	for _, n := range removed {
		if CopyTaskManager != nil {
			CopyTaskManager.Remove(idsByTask[n])
		}
		if removePartials {
			if err := removeNASPartial(n); err != nil {
				result.PartialFailures++
			}
		}
	}
	return result, nil
}

func startNASHistoryRetention() {
	// Run at startup and daily even if no browser is open. Keep failed partials;
	// record retention must never silently delete bytes on the NAS.
	_, _ = ClearNASHistory(nil, true, false, time.Now().Add(-NASHistoryDays*24*time.Hour))
	nasHistoryOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for now := range ticker.C {
				_, _ = ClearNASHistory(nil, true, false, now.Add(-NASHistoryDays*24*time.Hour))
			}
		}()
	})
}
