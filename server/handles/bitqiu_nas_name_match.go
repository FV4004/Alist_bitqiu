package handles

import (
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const inventoryMatchingVersion = "name-v1"

func normalizeInventoryOptions(o *nasInventoryOptions) {
	if o.Enabled {
		o.Matching = inventoryMatchingVersion
		o.CrossDirectory = true // Entire selected NAS scope; never outside its root.
	}
}

func inventoryName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("-_().[]", r) {
			return -1
		}
		return r
	}, compareFold(s))
}

func inventoryHashConflict(a, b nasInventoryObject) bool {
	if !a.HashVerified || !b.HashVerified || a.IsDir || b.IsDir {
		return false
	}
	for alg, value := range a.Hashes {
		if value != "" && b.Hashes[alg] != "" && value != b.Hashes[alg] {
			return true
		}
	}
	return false
}

// Folder context is supporting evidence only. Generic container names do not
// distinguish files; no child is linked just because its parent name matches.
func inventorySharedFolder(a, b string) string {
	names := map[string]bool{}
	generic := map[string]bool{"电影": true, "电视剧": true, "下载": true, "视频": true, "第一季": true, "movies": true, "tv": true, "downloads": true, "media": true, "season1": true}
	for _, n := range strings.Split(path.Dir(a), "/") {
		key := inventoryName(n)
		if key != "" && n != "." && !generic[key] {
			names[key] = true
		}
	}
	parts := strings.Split(path.Dir(b), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if names[inventoryName(parts[i])] {
			return parts[i]
		}
	}
	return ""
}

// Exact/normalized names and trusted hashes are indexed. Fuzzy names are bucketed
// by type, extension, digit sequences and normalized length, matching the existing
// conservative one-edit rule. Size alone never establishes a name relationship.
type inventoryNameIndex struct {
	target map[string]nasInventoryObject
	names  map[string][]string
	fuzzy  map[string][]string
	hashes map[string][]string
}

func inventoryTypeKey(v nasInventoryObject) string { return strconv.FormatBool(v.IsDir) + ":" }
func inventoryFuzzyKey(v nasInventoryObject, length int) string {
	name := compareFold(path.Base(v.Path))
	var digits strings.Builder
	in := false
	for _, r := range name {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
			in = true
		} else if in {
			digits.WriteByte('|')
			in = false
		}
	}
	return inventoryTypeKey(v) + path.Ext(name) + ":" + digits.String() + ":" + strconv.Itoa(length)
}
func newInventoryNameIndex(target map[string]nasInventoryObject) *inventoryNameIndex {
	idx := &inventoryNameIndex{target: target, names: map[string][]string{}, fuzzy: map[string][]string{}, hashes: map[string][]string{}}
	for p, v := range target {
		if p == "." {
			continue
		}
		name := inventoryName(path.Base(v.Path))
		key := inventoryTypeKey(v) + name
		idx.names[key] = append(idx.names[key], p)
		key = inventoryFuzzyKey(v, utf8.RuneCountInString(name))
		idx.fuzzy[key] = append(idx.fuzzy[key], p)
		if v.HashVerified && !v.IsDir {
			for alg, h := range v.Hashes {
				if h != "" {
					key = alg + ":" + h
					idx.hashes[key] = append(idx.hashes[key], p)
				}
			}
		}
	}
	return idx
}
func (idx *inventoryNameIndex) pool(a nasInventoryObject, work *int) (map[string]nasInventoryObject, error) {
	pool := map[string]nasInventoryObject{}
	add := func(paths []string) {
		for _, p := range paths {
			pool[p] = idx.target[p]
		}
	}
	name := inventoryName(path.Base(a.Path))
	add(idx.names[inventoryTypeKey(a)+name])
	length := utf8.RuneCountInString(name)
	for _, n := range []int{length - 1, length, length + 1} {
		add(idx.fuzzy[inventoryFuzzyKey(a, n)])
	}
	if a.HashVerified && !a.IsDir {
		for alg, h := range a.Hashes {
			if h != "" {
				add(idx.hashes[alg+":"+h])
			}
		}
	}
	// Include an occupied future download path even if its type conflicts.
	if v, ok := idx.target[a.Path]; ok {
		pool[a.Path] = v
	}
	*work += len(pool)
	if *work > 4000000 {
		return nil, errors.New("名称候选比较超过 400 万次，请缩小范围；旧完整结果已保留")
	}
	return pool, nil
}

// Old snapshots remain readable/exportable, but their decisions cannot authorize
// new reviews or downloads under a changed matching algorithm.
func inventoryRequireNameMatching(j *nasScanJob) error {
	if j.Options.Matching != inventoryMatchingVersion {
		return errors.New("匹配规则已升级为全范围名称匹配，旧结果仅供查看和导出；请重新盘点复核")
	}
	return nil
}
