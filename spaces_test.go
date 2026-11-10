// Modified for Claude Swap Command (2026).
package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSyncSpaces(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "spaces-seen.json")
	write(t, filepath.Join(root, "a", "oa", "spaces.json"), `{"spaces":[
		{"id":"web","name":"Web v2","folders":[{"path":"/src/web"}],"projects":[{"uuid":"pa"}],"migration":{"stage":"complete"},"links":[],"createdAt":1,"updatedAt":300,"future":true},
		{"id":"api","name":"API","folders":[{"path":"/src/api"}],"projects":[{"uuid":"pb"}],"links":[],"createdAt":1,"updatedAt":100}]}`)
	write(t, filepath.Join(root, "b", "ob", "spaces.json"), `{"spaces":[
		{"id":"web","name":"Web","folders":[{"path":"/src/web"}],"projects":[{"uuid":"own"}],"links":[],"createdAt":1,"updatedAt":200}],"version":2}`)

	n, err := syncSpaces(root, "b", "ob", seen, ProjectTransferApproval{Project: "/src/web", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "b", TargetOrg: "ob"}, ProjectTransferApproval{Project: "/src/api", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "b", TargetOrg: "ob"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("wrote %d projects, want 2 (newer web, new api)", n)
	}
	file, got := spacesByID(t, filepath.Join(root, "b", "ob", "spaces.json"))
	if string(file["version"]) != "2" {
		t.Fatal("other keys of the file were lost")
	}
	web := got["web"]
	if web["name"] != "Web v2" || web["future"] != true {
		t.Fatalf("web not refreshed: %v", web)
	}
	if p := web["projects"].([]any); len(p) != 1 || p[0].(map[string]any)["uuid"] != "own" {
		t.Fatalf("web lost its own org's projects: %v", web["projects"])
	}
	if _, ok := web["migration"]; ok {
		t.Fatal("web took the other org's migration")
	}
	if _, ok := got["api"]["projects"]; ok {
		t.Fatal("api kept the other org's projects")
	}
	if n, _ := syncSpaces(root, "b", "ob", seen, ProjectTransferApproval{Project: "/src/web", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "b", TargetOrg: "ob"}, ProjectTransferApproval{Project: "/src/api", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "b", TargetOrg: "ob"}); n != 0 {
		t.Fatalf("second sync wrote %d, want 0", n)
	}

	// A project deleted in b stays deleted.
	write(t, filepath.Join(root, "b", "ob", "spaces.json"), `{"spaces":[]}`)
	if n, _ := syncSpaces(root, "b", "ob", seen, ProjectTransferApproval{Project: "/src/web", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "b", TargetOrg: "ob"}, ProjectTransferApproval{Project: "/src/api", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "b", TargetOrg: "ob"}); n != 0 {
		t.Fatalf("deleted projects came back, wrote %d", n)
	}

	// A brand new account gets the file.
	if n, _ := syncSpaces(root, "c", "oc", seen, ProjectTransferApproval{Project: "/src/web", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "c", TargetOrg: "oc"}, ProjectTransferApproval{Project: "/src/api", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "c", TargetOrg: "oc"}); n != 2 {
		t.Fatalf("new account got %d, want 2", n)
	}

	// An unreadable file is left alone.
	bad := filepath.Join(root, "d", "od", "spaces.json")
	write(t, bad, "{not json")
	if _, err := syncSpaces(root, "d", "od", seen, ProjectTransferApproval{Project: "/src/web", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "d", TargetOrg: "od"}, ProjectTransferApproval{Project: "/src/api", SourceAccount: "a", SourceOrg: "oa", TargetAccount: "d", TargetOrg: "od"}); err == nil {
		t.Fatal("no error for an unreadable spaces file")
	}
	if read(t, bad) != "{not json" {
		t.Fatal("unreadable spaces file was overwritten")
	}
}

func spacesByID(t *testing.T, path string) (map[string]json.RawMessage, map[string]map[string]any) {
	t.Helper()
	var file map[string]json.RawMessage
	if err := json.Unmarshal([]byte(read(t, path)), &file); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal(file["spaces"], &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, sp := range list {
		out[sp["id"].(string)] = sp
	}
	return file, out
}

func TestSelectedSpacesPreserveOpaqueAndUnrelatedGroups(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "seen.json")
	write(t, filepath.Join(root, "source", "org", "spaces.json"), `{"spaces":[{"id":"selected","folders":[{"path":"/work/selected"}],"updatedAt":5},{"id":"mixed","folders":[{"path":"/work/selected"},{"path":"/personal/private"}],"updatedAt":5}]}`)
	target := filepath.Join(root, "target", "org", "spaces.json")
	write(t, target, `{"version":8,"spaces":[{"futureOpaque":true},{"id":"unrelated","folders":[{"path":"/other"}],"keep":true}]}`)
	a := ProjectTransferApproval{Project: "/work/selected", SourceAccount: "source", SourceOrg: "org", TargetAccount: "target", TargetOrg: "org"}
	if n, e := syncSpaces(root, "target", "org", seen, a); n != 1 || e != nil {
		t.Fatalf("scoped spaces=%d %v", n, e)
	}
	var file struct {
		Version int
		Spaces  []map[string]any
	}
	if e := json.Unmarshal([]byte(read(t, target)), &file); e != nil {
		t.Fatal(e)
	}
	if file.Version != 8 || len(file.Spaces) != 3 || file.Spaces[0]["futureOpaque"] != true || file.Spaces[1]["keep"] != true {
		t.Fatal("unrelated or opaque space changed")
	}
}
