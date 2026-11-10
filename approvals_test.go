package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
)

func TestRecordProjectPathsCoverEveryFolderField(t *testing.T) {
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{
		"cwd": "/work/app", "originCwd": "/work/origin", "worktreePath": "/work/tree", "gitAnchorsFolderRealpath": "/work/anchor",
		"shellFolders": ["/work/shell-a", "relative/ignored", "/work/unclean/../ignored"],
		"requestedAdditionalFolders": [{"path": "/work/extra"}],
		"folderWorktrees": [{"folder": "/work/folder", "worktreePath": "/work/folder-tree", "repoRoot": "/work/repo"}],
		"title": "not a path"
	}`), &record); err != nil {
		t.Fatal(err)
	}
	got := recordProjectPaths(record)
	sort.Strings(got)
	want := []string{"/work/anchor", "/work/app", "/work/extra", "/work/folder", "/work/folder-tree", "/work/origin", "/work/repo", "/work/shell-a", "/work/tree"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

// Project groups follow the same idea as conversations: every group of the
// account being left arrives on the account being opened, a newer destination
// group is kept, and nothing the destination has is removed.
func TestCarryProjectGroupsBringsEveryGroupAndKeepsNewerOnes(t *testing.T) {
	f := newCarryFixture(t)
	f.put(f.src, 1, map[string]any{"cwd": "/src/web", "spaceId": "web"})
	spaces := func(ns carryNamespace) string {
		return filepath.Join(f.d.spacesDir(), ns.Account, ns.Org, "spaces.json")
	}
	write(t, spaces(f.src), `{"spaces":[
		{"id":"web","name":"Web (older)","folders":[{"path":"/src/web"}],"links":[],"createdAt":1,"updatedAt":100},
		{"id":"api","name":"API","folders":[{"path":"/src/api"},{"path":"/src/shared"}],"links":[],"createdAt":1,"updatedAt":100}]}`)
	write(t, spaces(f.dst), `{"spaces":[
		{"id":"web","name":"Web (newer here)","folders":[{"path":"/src/web"}],"links":[],"createdAt":1,"updatedAt":900},
		{"id":"own","name":"Only here","folders":[{"path":"/src/own"}],"links":[],"createdAt":1,"updatedAt":50}],"version":2}`)

	approvals, err := carryApprovals(f.d, f.src, f.dst)
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, a := range approvals {
		if a.SourceAccount != f.src.Account || a.SourceOrg != f.src.Org || a.TargetAccount != f.dst.Account || a.TargetOrg != f.dst.Org {
			t.Fatalf("a route must lead from the source to the destination only: %+v", a)
		}
		routes = append(routes, a.Project)
	}
	if fmt.Sprint(routes) != "[/src/api /src/shared /src/web]" {
		t.Fatalf("routes = %v", routes)
	}

	n, err := carryProjectGroups(f.d, f.src, f.dst, filepath.Join(f.root, "spaces-seen.json"))
	if err != nil || n != 1 {
		t.Fatalf("carried %d groups, %v; want only the new API group", n, err)
	}
	_, got := spacesByID(t, spaces(f.dst))
	if got["web"]["name"] != "Web (newer here)" || got["api"]["name"] != "API" || got["own"]["name"] != "Only here" {
		t.Fatalf("groups after the carry: %v", got)
	}

	// An account without project groups, or without a folder at all, is fine.
	empty := newCarryFixture(t)
	if n, err := carryProjectGroups(empty.d, empty.src, empty.dst, filepath.Join(empty.root, "spaces-seen.json")); err != nil || n != 0 {
		t.Fatalf("nothing to carry is not an error: %d %v", n, err)
	}
}
