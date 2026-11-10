package main

// Project groups ("spaces") travel with the conversations. The group merge in
// spaces.go works from a list of folder routes; for a switch made from the
// menu the helper writes that list itself: one route per folder named by the
// source account's conversations and groups, from the source to the
// destination, for this switch only. Nothing is saved and nobody is asked.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// recordProjectPaths lists every absolute folder a conversation record names.
func recordProjectPaths(record map[string]json.RawMessage) []string {
	var paths []string
	add := func(raw json.RawMessage) {
		var p string
		if json.Unmarshal(raw, &p) == nil && filepath.IsAbs(p) && filepath.Clean(p) == p {
			paths = append(paths, p)
		}
	}
	for _, key := range []string{"cwd", "originCwd", "worktreePath", "gitAnchorsFolderRealpath"} {
		if raw, ok := record[key]; ok {
			add(raw)
		}
	}
	if raw, ok := record["shellFolders"]; ok {
		var folders []json.RawMessage
		if json.Unmarshal(raw, &folders) == nil {
			for _, folder := range folders {
				add(folder)
			}
		}
	}
	if raw, ok := record["requestedAdditionalFolders"]; ok {
		var extra []map[string]json.RawMessage
		if json.Unmarshal(raw, &extra) == nil {
			for _, folder := range extra {
				add(folder["path"])
			}
		}
	}
	if raw, ok := record["folderWorktrees"]; ok {
		var folders []map[string]json.RawMessage
		if json.Unmarshal(raw, &folders) == nil {
			for _, folder := range folders {
				add(folder["folder"])
				add(folder["worktreePath"])
				add(folder["repoRoot"])
			}
		}
	}
	return paths
}

// namespaceProjectPaths lists every folder named by one account's
// conversation records and by its project groups, sorted.
func namespaceProjectPaths(d desktop, ns carryNamespace) ([]string, error) {
	if !ns.valid() {
		return nil, coded("CARRY_ACCOUNTS_INVALID", "the account's conversation folder could not be identified")
	}
	seen := map[string]bool{}
	var out []string
	note := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	records, _ := filepath.Glob(filepath.Join(d.sessionsDir(), ns.Account, ns.Org, "local_*.json"))
	for _, path := range records {
		data, err := readTransferMetadata(path)
		if err != nil {
			continue
		}
		var record map[string]json.RawMessage
		if json.Unmarshal(data, &record) != nil {
			continue
		}
		for _, p := range recordProjectPaths(record) {
			note(p)
		}
	}
	_, spaces, err := readSpaces(filepath.Join(d.spacesDir(), ns.Account, ns.Org, "spaces.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, codedf("PROJECT_GROUPS_UNREADABLE", "the project groups could not be read (%w)", err)
	}
	for _, sp := range spaces {
		var folders []struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(sp["folders"], &folders) == nil {
			for _, folder := range folders {
				if filepath.IsAbs(folder.Path) {
					note(filepath.Clean(folder.Path))
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// carryApprovals routes every folder of the source account to the destination.
func carryApprovals(d desktop, src, dst carryNamespace) ([]ProjectTransferApproval, error) {
	if !src.valid() || !dst.valid() || src == dst {
		return nil, coded("CARRY_ACCOUNTS_INVALID", "the two accounts to carry project groups between could not be identified")
	}
	paths, err := namespaceProjectPaths(d, src)
	if err != nil {
		return nil, err
	}
	approvals := make([]ProjectTransferApproval, 0, len(paths))
	for _, p := range paths {
		approvals = append(approvals, ProjectTransferApproval{Project: p, SourceAccount: src.Account, SourceOrg: src.Org, TargetAccount: dst.Account, TargetOrg: dst.Org})
	}
	return approvals, nil
}

// carryProjectGroups merges the source account's project groups into the
// destination's inside the switch's backup. A newer destination group is
// kept, a group the destination removed earlier stays removed, and nothing
// the destination has is taken away.
func carryProjectGroups(d desktop, src, dst carryNamespace, seenPath string) (int, error) {
	approvals, err := carryApprovals(d, src, dst)
	if err != nil {
		return 0, err
	}
	if len(approvals) == 0 {
		return 0, nil
	}
	if _, err := os.Lstat(filepath.Join(d.spacesDir(), src.Account, src.Org, "spaces.json")); errors.Is(err, os.ErrNotExist) {
		return 0, nil // the source has no project groups
	}
	n, err := syncSpaces(d.spacesDir(), dst.Account, dst.Org, seenPath, approvals...)
	if err != nil {
		return 0, codedf("PROJECT_GROUPS_FAILED", "the project groups could not be carried (%w)", err)
	}
	return n, nil
}

// An approval belongs to one source organization and one destination. A local
// folder alone is not permission to import that folder from every account.
type ProjectTransferApproval struct {
	Project       string `json:"project"`
	SourceAccount string `json:"sourceAccount"`
	SourceOrg     string `json:"sourceOrg"`
	TargetAccount string `json:"targetAccount"`
	TargetOrg     string `json:"targetOrg"`
}

func transferDestination(approvals []ProjectTransferApproval, targetAccount, targetOrg string) bool {
	for _, a := range approvals {
		if a.TargetAccount == targetAccount && a.TargetOrg == targetOrg && filepath.IsAbs(a.Project) {
			return true
		}
	}
	return false
}

func transferAllows(approvals []ProjectTransferApproval, sourceAccount, sourceOrg, targetAccount, targetOrg, project string) bool {
	if !filepath.IsAbs(project) {
		return false
	}
	for _, a := range approvals {
		if a.SourceAccount == sourceAccount && a.SourceOrg == sourceOrg && a.TargetAccount == targetAccount && a.TargetOrg == targetOrg && transferProjectPath(a.Project) == transferProjectPath(project) {
			return true
		}
	}
	return false
}

func transferProjectPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func transferSource(approvals []ProjectTransferApproval, sourceAccount, sourceOrg, targetAccount, targetOrg string) bool {
	for _, a := range approvals {
		if a.SourceAccount == sourceAccount && a.SourceOrg == sourceOrg && a.TargetAccount == targetAccount && a.TargetOrg == targetOrg && filepath.IsAbs(a.Project) {
			return true
		}
	}
	return false
}

func readTransferMetadata(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("session metadata is unavailable or unsupported")
	}
	data, _, err := readStableFile(path)
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("session metadata changed while reading")
	}
	return data, nil
}

// A space may contain several folders. Never carry an unapproved folder into
// the destination merely because another folder in the space was selected.
func transferSpaceAllowed(sp space, approvals []ProjectTransferApproval, sourceAccount, sourceOrg, targetAccount, targetOrg string) bool {
	var folders []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(sp["folders"], &folders) != nil || len(folders) == 0 {
		return false
	}
	for _, folder := range folders {
		if !transferAllows(approvals, sourceAccount, sourceOrg, targetAccount, targetOrg, folder.Path) {
			return false
		}
	}
	return true
}

func transferDirIdentity(dir string) (string, string) {
	return filepath.Base(filepath.Dir(dir)), filepath.Base(dir)
}

func validTransferPathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}
