package handles

import (
	"fmt"
	"reflect"
	"testing"
)

func TestInventoryNameMatchingDifferentLayouts(t *testing.T) {
	source := nasInventoryObject{Path: "收藏/电影/流浪地球2/流浪地球2.mkv", Size: 100, SizeKnown: true}
	objects := []nasInventoryObject{
		{Path: source.Path, Size: 1, SizeKnown: true},
		{Path: "media/国产科幻/流浪地球2/流浪地球2.mkv", Size: 100, SizeKnown: true},
		{Path: "备份/流浪地球2.mkv", Size: 100, SizeKnown: true},
		{Path: "其他/流浪地球2.mkv", SizeKnown: false},
	}
	target := map[string]nasInventoryObject{}
	for _, v := range objects {
		target[v.Path] = v
	}
	kind, direct, candidates, _ := inventoryMatch(source, target, false)
	if kind != "size_difference" || direct == nil || len(candidates) != 4 {
		t.Fatalf("occupied path hid candidates: %s %+v", kind, candidates)
	}
	if candidates[0].Object.Path != objects[1].Path || candidates[1].Object.Path != objects[2].Path || candidates[3].Object.Path != source.Path {
		t.Fatalf("evidence ranking: %+v", candidates)
	}
	if inventorySharedFolder(source.Path, objects[1].Path) != "流浪地球2" {
		t.Fatal("lost folder context")
	}
	if inventorySharedFolder("电影/a.mkv", "archive/电影/a.mkv") != "" {
		t.Fatal("generic folder treated as distinctive")
	}
	// A different folder layout cannot cause a file to disappear; flattened NAS works.
	delete(target, source.Path)
	_, direct, candidates, _ = inventoryMatch(source, target, false)
	if direct != nil || len(candidates) != 3 {
		t.Fatal("legacy cross-directory flag restricted global names")
	}
}

func TestInventoryNameIndexMatchesExhaustiveSearch(t *testing.T) {
	target := map[string]nasInventoryObject{}
	names := []string{"Movie Name.mkv", "movie_name.mkv", "MOVIE-NAME.mkv", "movie.names.mkv", "movie name.mp4", "movie name2.mkv", "S01E01.mkv", "S01E02.mkv", "目录", "目录_", "[归档]", "新名字.bin", "Ｍｏｖｉｅ　Ｎａｍｅ.mkv"}
	for i, name := range names {
		v := nasInventoryObject{Path: fmt.Sprintf("media/%d/%s", i, name), IsDir: i >= 8 && i <= 10, Size: 20, SizeKnown: true}
		if i == 11 {
			v.HashVerified = true
			v.Hashes = map[string]string{"sha256": "trusted"}
		}
		target[v.Path] = v
	}
	index := newInventoryNameIndex(target)
	for _, name := range append(names, "completely-different.bin") {
		for _, dir := range []bool{false, true} {
			a := nasInventoryObject{Path: "cloud/unrelated/" + name, IsDir: dir, Size: 20, SizeKnown: true, HashVerified: true, Hashes: map[string]string{"sha256": "trusted"}}
			wantKind, wantDirect, want, wantReasons := inventoryMatch(a, target, true)
			work := 0
			pool, err := index.pool(a, &work)
			if err != nil {
				t.Fatal(err)
			}
			kind, direct, got, reasons := inventoryMatchPool(a, pool)
			if kind != wantKind || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(direct, wantDirect) || !reflect.DeepEqual(reasons, wantReasons) {
				t.Fatalf("index lost evidence for %s dir=%v: %+v / %+v", name, dir, got, want)
			}
		}
	}
	// Unrelated extensions/digit sequences must not be compared as a full matrix.
	work := 0
	_, _ = index.pool(nasInventoryObject{Path: "a/S01E01.mkv"}, &work)
	if work >= len(target) {
		t.Fatal("index did not narrow fuzzy search")
	}
	work = 4000000
	if _, err := index.pool(nasInventoryObject{Path: "a/S01E01.mkv"}, &work); err == nil {
		t.Fatal("comparison budget not enforced")
	}
}

func TestInventoryNameCandidatesNeverProveContent(t *testing.T) {
	a := nasInventoryObject{Path: "cloud/Series/S01E01.mkv", Size: 0, SizeKnown: false, HashVerified: false, Hashes: map[string]string{"md5": "reported"}}
	target := map[string]nasInventoryObject{
		"nas/Series/S01E01.mkv": {Path: "nas/Series/S01E01.mkv", Size: 0, SizeKnown: true, HashVerified: true, Hashes: a.Hashes},
		"nas/Series/S01E02.mkv": {Path: "nas/Series/S01E02.mkv", Size: 0, SizeKnown: true},
	}
	kind, _, candidates, _ := inventoryMatch(a, target, false)
	if kind != "suspected_move" || len(candidates) != 1 || inventoryHashMatch(a, candidates[0].Object) {
		t.Fatalf("unverified/name evidence promoted or episode mixed: %s %+v", kind, candidates)
	}
	a.HashVerified = true
	a.Hashes = map[string]string{"md5": "same", "sha256": "different"}
	b := a
	b.Hashes = map[string]string{"md5": "same", "sha256": "other"}
	if inventoryHashMatch(a, b) || !inventoryHashConflict(a, b) {
		t.Fatal("one equal digest hid conflicting trusted digest")
	}
	// Parent folder agreement never manufactures an unrelated child candidate.
	_, _, candidates, _ = inventoryMatch(nasInventoryObject{Path: "cloud/Distinctive/unrelated.bin"}, target, true)
	if len(candidates) != 0 {
		t.Fatal("folder-only file association")
	}
}

func TestInventoryNameMatchingVersionIsolation(t *testing.T) {
	old := nasInventoryOptions{Enabled: true}
	if inventoryRequireNameMatching(&nasScanJob{Options: old}) == nil {
		t.Fatal("old review can authorize new operations")
	}
	current := old
	normalizeInventoryOptions(&current)
	if !current.CrossDirectory || current.Matching != inventoryMatchingVersion {
		t.Fatal("new scan retained legacy path constraint")
	}
	if inventoryDigest(old) == inventoryDigest(current) {
		t.Fatal("old and new scopes share cache identity")
	}
	if err := inventoryRequireNameMatching(&nasScanJob{Options: current}); err != nil {
		t.Fatal(err)
	}
}
