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
		idsByTask[n] = n.record.ID
		groups[key] = append(groups[key], n)
		n.mu.Unlock()
	}
	nasRegistry.Unlock()
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		members := groups[key]
		sort.Slice(members, func(i, j int) bool { return idsByTask[members[i]] < idsByTask[members[j]] })
		chosen := all || selected[key]
		for _, n := range members {
			if selected[idsByTask[n]] {
				chosen = true
			}
		}
		if !chosen {
			continue
		}
		for _, n := range members {
			n.action.Lock()
		}
		nasSaveMu.Lock()
		nasRegistry.Lock()
		valid := true
		for _, n := range members {
			n.mu.Lock()
			r := n.record
			if nasRegistry.tasks[r.ID] != n || n.active || !nasHistoryState(r.State) || (!cutoff.IsZero() && (r.EndTime == nil || !r.EndTime.Before(cutoff))) {
				valid = false
			}
			n.mu.Unlock()
		}
		// A group is immutable after submission, so it cannot gain new children here.
		if valid {
			for _, n := range members {
				delete(nasRegistry.tasks, n.record.ID)
			}
		}
		nasRegistry.Unlock()
		var saveErr error
		if valid {
			saveErr = saveNASDownloadsLocked()
			if saveErr != nil {
				nasRegistry.Lock()
				for _, n := range members {
					nasRegistry.tasks[n.record.ID] = n
				}
				nasRegistry.Unlock()
			}
		}
		nasSaveMu.Unlock()
		if valid && saveErr == nil {
			result.Deleted += len(members)
			if removePartials {
				for _, n := range members {
					if err := removeNASPartial(n); err != nil {
						result.PartialFailures++
					}
				}
			}
		} else {
			result.Skipped += len(members)
		}
		for i := len(members) - 1; i >= 0; i-- {
			members[i].action.Unlock()
		}
		if saveErr != nil {
			return result, errors.New("清理记录保存失败，记录已恢复")
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
