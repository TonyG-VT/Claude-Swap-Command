package main

// Reading and writing Claude's conversation record files safely.
//
// A record is only ever read as a plain, single-link file of bounded size
// that did not change while it was read, and only ever written inside the
// switch's own backup, over exactly the bytes the plan saw.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	recordSizeLimit     = 1 << 20 // one conversation record
	queuedManifestLimit = 4 << 20 // one waiting-input file
)

var idPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

// validID reports whether id is an identifier the way Claude writes them:
// lower case, in the usual 8-4-4-4-12 form.
func validID(id string) bool {
	return id == strings.ToLower(id) && idPattern.MatchString(id)
}

var errRecordFile = errors.New("a conversation file is not a plain file of the expected kind")

// jsonUnambiguous reports whether data is JSON with one interpretation.
// encoding/json accepts repeated fields, including aliases that differ only
// in case, by replacing or merging the earlier value; such a record is not
// trusted. Depth and object width are bounded here.
func jsonUnambiguous(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, container := token.(json.Delim)
		if !container {
			return true
		}
		switch delim {
		case '{':
			var seen []string
			for d.More() {
				field, err := d.Token()
				key, ok := field.(string)
				if err != nil || !ok || len(seen) >= 128 {
					return false
				}
				for _, previous := range seen {
					if strings.EqualFold(previous, key) {
						return false
					}
				}
				seen = append(seen, key)
				if !value(depth + 1) {
					return false
				}
			}
			token, err = d.Token()
			return err == nil && token == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			token, err = d.Token()
			return err == nil && token == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// readRecordFile reads one file below Claude's data folder. The path must be
// absolute and clean, no folder on the way may be a link, and the file must
// be a regular, single-link file no larger than limit that does not change
// while it is read. With absent set, a missing file is an empty result.
func readRecordFile(path string, absent bool, limit int64) ([]byte, fileFingerprint, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fileFingerprint{}, errRecordFile
	}
	for cur := path; cur != filepath.Dir(cur); cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fileFingerprint{}, errRecordFile
		}
	}
	before, err := os.Lstat(path)
	if absent && errors.Is(err, os.ErrNotExist) {
		return nil, fileFingerprint{}, nil
	}
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > limit {
		return nil, fileFingerprint{}, errRecordFile
	}
	if st, ok := before.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return nil, fileFingerprint{}, errRecordFile
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fileFingerprint{}, errRecordFile
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, fileFingerprint{}, errRecordFile
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	after, ae := f.Stat()
	current, ce := os.Lstat(path)
	if err != nil || ae != nil || ce != nil || !os.SameFile(opened, current) || !current.Mode().IsRegular() || after.Size() != opened.Size() || after.ModTime() != opened.ModTime() || int64(len(data)) != after.Size() || int64(len(data)) > limit {
		return nil, fileFingerprint{}, errRecordFile
	}
	return data, fingerprintBytes(data, after), nil
}

// validQueuedManifest reports whether data is a waiting-input file of the
// form Claude writes for conversation id.
func validQueuedManifest(data []byte, id string) bool {
	if len(data) == 0 || len(data) > queuedManifestLimit || !jsonUnambiguous(data) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	var version int
	var nativeID string
	var entries []json.RawMessage
	if json.Unmarshal(fields["v"], &version) != nil || version != 1 || json.Unmarshal(fields["sessionId"], &nativeID) != nil || nativeID != "local_"+id || json.Unmarshal(fields["entries"], &entries) != nil || entries == nil || len(entries) > 512 {
		return false
	}
	clean := bytes.TrimSpace(fields["cleanShutdown"])
	return bytes.Equal(clean, []byte("true")) || bytes.Equal(clean, []byte("false"))
}

// writeRecordInBackup writes one conversation record, and only inside the
// backup that is open right now for this Claude data folder. The file on
// disk must still be exactly what the plan saw (expected), it must not have
// been written by this switch before, and what is about to be written is
// noted in the backup before the file is replaced.
func writeRecordInBackup(d desktop, path string, data []byte, source, expected fileFingerprint, backupDir string) error {
	if int64(len(data)) > recordSizeLimit {
		return errRecordFile
	}
	tmp, after, err := stageProtectedFile(path, data, &source)
	if err != nil {
		return errRecordFile
	}
	defer os.Remove(tmp)
	root, err := canonicalDirectory(d.dir)
	if err != nil {
		return errRecordFile
	}
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	tx := backupRegistry.active[root]
	if tx == nil || tx.dir != backupDir || tx.manifest.Phase != "prepared" && tx.manifest.Phase != "mutating" {
		return errRecordFile
	}
	scope, rel, within, err := tx.classify(path)
	if err != nil || !within || scope != "desktop" {
		return errRecordFile
	}
	_, current, err := readRecordFile(path, true, recordSizeLimit)
	if err != nil || current != expected {
		return errRecordFile
	}
	var entry *backupEntry
	for i := range tx.manifest.Entries {
		if tx.manifest.Entries[i].Scope == scope && tx.manifest.Entries[i].Path == rel {
			entry = &tx.manifest.Entries[i]
			break
		}
	}
	if entry == nil {
		if expected.Exists {
			return errRecordFile
		}
		tx.manifest.Entries = append(tx.manifest.Entries, backupEntry{Scope: scope, Path: rel})
		entry = &tx.manifest.Entries[len(tx.manifest.Entries)-1]
	}
	if entry.Before != expected || len(entry.Intents) != 0 {
		return errRecordFile
	}
	if current == after {
		return nil
	}
	entry.Intents = append(entry.Intents, after)
	tx.manifest.Phase = "mutating"
	if tx.persist() != nil {
		return errRecordFile
	}
	if os.Rename(tmp, path) != nil || syncDirectory(filepath.Dir(path)) != nil {
		return errRecordFile
	}
	return nil
}
