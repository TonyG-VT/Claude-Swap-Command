// Modified for Claude Swap Command (2026).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Projects in the app (spaces internally) are kept per account in
// local-agent-mode-sessions/<account>/<org>/spaces.json. A chat in a project
// names it by spaceId, so a chat copied to another account only shows in its
// project once that account has the project too.
//
// projects and migration tie a space to claude.ai projects of the org that
// made it. The other org cannot open those, so a copy leaves them out and
// the app sets the space up for its own org.
var orgBoundSpaceKeys = []string{"projects", "migration"}

type space = map[string]json.RawMessage

// syncSpaces copies the projects of every other account into the target
// account's spaces.json. It adds missing projects and refreshes older ones,
// it never deletes. seenPath remembers which projects each account had, so a
// project the user deleted in one account is not brought back. Returns how
// many projects it wrote.
func syncSpaces(root, account, org, seenPath string, approvals ...ProjectTransferApproval) (int, error) {
	if !transferDestination(approvals, account, org) {
		return 0, nil
	}
	if !validTransferPathComponent(account) || !validTransferPathComponent(org) {
		return 0, errors.New("invalid transfer destination")
	}
	targetPath := filepath.Join(root, account, org, "spaces.json")
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "spaces.json"))
	if err != nil {
		return 0, err
	}

	newest := map[string]space{}
	var order []string
	for _, p := range paths {
		if samePath(p, targetPath) {
			continue
		}
		sourceAccount, sourceOrg := transferDirIdentity(filepath.Dir(p))
		if !transferSource(approvals, sourceAccount, sourceOrg, account, org) {
			continue
		}
		_, spaces, err := readSpaces(p)
		if err != nil {
			continue
		}
		for _, sp := range spaces {
			if !transferSpaceAllowed(sp, approvals, sourceAccount, sourceOrg, account, org) {
				continue
			}
			id := spaceID(sp)
			if id == "" {
				continue
			}
			if cur, ok := newest[id]; !ok {
				order = append(order, id)
				newest[id] = sp
			} else if updatedAt(sp) > updatedAt(cur) {
				newest[id] = sp
			}
		}
	}

	file, spaces, err := readSpaces(targetPath)
	if errors.Is(err, os.ErrNotExist) {
		file = map[string]json.RawMessage{}
	} else if err != nil {
		// Leave a file the app cannot read either to the app's own recovery.
		return 0, err
	}
	key := account + "/" + org
	seen := readSeen(seenPath)
	had := map[string]bool{}
	for _, id := range seen[key] {
		had[id] = true
	}
	index := map[string]int{}
	for i, sp := range spaces {
		index[spaceID(sp)] = i
	}

	written := 0
	for _, id := range order {
		src := newest[id]
		i, ok := index[id]
		switch {
		case ok && updatedAt(src) > updatedAt(spaces[i]):
			// Do not overwrite a destination group whose folders do not match
			// any approved route, even if a source happens to reuse its ID.
			approved := false
			for _, a := range approvals {
				if transferSpaceAllowed(spaces[i], approvals, a.SourceAccount, a.SourceOrg, account, org) {
					approved = true
					break
				}
			}
			if !approved {
				continue
			}
			spaces[i] = refreshSpace(spaces[i], src)
		case !ok && !had[id]:
			spaces = append(spaces, copySpace(src))
			index[id] = len(spaces) - 1
		default:
			continue
		}
		written++
	}

	if written > 0 {
		list, err := json.Marshal(spaces)
		if err != nil {
			return 0, err
		}
		file["spaces"] = list
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return 0, err
		}
		if err := writeFileAtomic(targetPath, data); err != nil {
			return 0, err
		}
	}

	ids := make([]string, 0, len(spaces))
	for _, sp := range spaces {
		ids = append(ids, spaceID(sp))
	}
	seen[key] = ids
	return written, writeSeen(seenPath, seen)
}

// readSpaces retains opaque entries too; a scoped import may not erase an
// unrelated future-format entry just because this release cannot identify it.
func readSpaces(path string) (map[string]json.RawMessage, []space, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}
	var all []space
	if raw, ok := file["spaces"]; ok {
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, nil, err
		}
	}
	return file, all, nil
}

func spaceID(sp space) string {
	var id string
	_ = json.Unmarshal(sp["id"], &id)
	return id
}

func updatedAt(sp space) float64 {
	var at float64
	_ = json.Unmarshal(sp["updatedAt"], &at)
	return at
}

// copySpace is src without the parts that belong to its org.
func copySpace(src space) space {
	out := space{}
	for k, v := range src {
		out[k] = v
	}
	for _, k := range orgBoundSpaceKeys {
		delete(out, k)
	}
	return out
}

// refreshSpace takes the newer content of src and keeps dst's own org parts.
func refreshSpace(dst, src space) space {
	out := copySpace(src)
	for _, k := range orgBoundSpaceKeys {
		if v, ok := dst[k]; ok {
			out[k] = v
		}
	}
	return out
}

func readSeen(path string) map[string][]string {
	seen := map[string][]string{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
	}
	return seen
}

func writeSeen(path string, seen map[string][]string) error {
	data, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
