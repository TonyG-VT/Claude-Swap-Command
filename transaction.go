package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const transactionVersion = 1

type backupEntry struct {
	Scope    string            `json:"scope"`
	Path     string            `json:"path"`
	Snapshot string            `json:"snapshot,omitempty"`
	Before   fileFingerprint   `json:"before"`
	Intents  []fileFingerprint `json:"intents,omitempty"`
	Sealed   *fileFingerprint  `json:"sealed,omitempty"`
}

type backupManifest struct {
	Version     int           `json:"version"`
	DesktopRoot string        `json:"desktopRoot"`
	AuxRoot     string        `json:"auxRoot"`
	Created     time.Time     `json:"created"`
	Phase       string        `json:"phase"`
	Entries     []backupEntry `json:"entries"`
}

type backupTransaction struct {
	dir          string
	desktopAlias string
	auxAlias     string
	manifest     backupManifest
}

var backupRegistry = struct {
	sync.Mutex
	active map[string]*backupTransaction
}{active: make(map[string]*backupTransaction)}

func canonicalDirectory(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("expected an existing unlinked directory")
	}
	return filepath.EvalSymlinks(path)
}

func desktopMetadataPath(rel string) bool {
	switch filepath.ToSlash(rel) {
	case "config.json", "Cookies", "Cookies-journal", "Cookies-wal", "Cookies-shm",
		"Network/Cookies", "Network/Cookies-journal", "Network/Cookies-wal", "Network/Cookies-shm":
		return true
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 1 && parts[0] == "claude-code-sessions" {
		return true
	}
	return len(parts) == 4 && parts[0] == "local-agent-mode-sessions" && parts[3] == "spaces.json"
}

func safeRelative(root, rel string) (string, error) {
	if rel == "." || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid backup-relative path")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing linked backup path: %s", current)
		}
	}
	return filepath.Join(root, rel), nil
}

func relativeWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	return rel, err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (tx *backupTransaction) target(scope, rel string) (string, error) {
	switch scope {
	case "desktop":
		if !desktopMetadataPath(rel) {
			return "", errors.New("backup contains an out-of-scope Desktop path")
		}
		return safeRelative(tx.manifest.DesktopRoot, rel)
	case "aux":
		if rel != "spaces-seen.json" {
			return "", errors.New("backup contains an unsupported auxiliary path")
		}
		return safeRelative(tx.manifest.AuxRoot, rel)
	default:
		return "", errors.New("unknown backup path scope")
	}
}

func (tx *backupTransaction) classify(path string) (string, string, bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", false, err
	}
	for _, root := range []string{tx.desktopAlias, tx.manifest.DesktopRoot} {
		if rel, within := relativeWithin(root, absolute); within {
			if !desktopMetadataPath(rel) {
				return "", "", true, errors.New("transaction refuses a write outside Desktop metadata")
			}
			_, err := tx.target("desktop", rel)
			return "desktop", rel, true, err
		}
	}
	for _, entry := range tx.manifest.Entries {
		if entry.Scope != "aux" {
			continue
		}
		for _, root := range []string{tx.auxAlias, tx.manifest.AuxRoot} {
			if absolute == filepath.Join(root, entry.Path) {
				_, err := tx.target("aux", entry.Path)
				return "aux", entry.Path, true, err
			}
		}
	}
	return "", "", false, nil
}

func (tx *backupTransaction) persist() error {
	data, err := json.MarshalIndent(tx.manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileRaw(filepath.Join(tx.dir, "manifest.json"), data, nil)
}

func (tx *backupTransaction) addSnapshot(scope, rel string) error {
	path, err := tx.target(scope, rel)
	if err != nil {
		return err
	}
	data, state, err := readStableFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entry := backupEntry{Scope: scope, Path: rel, Before: state}
	if state.Exists {
		entry.Snapshot = filepath.Join("files", fmt.Sprintf("%06d", len(tx.manifest.Entries)))
		if err := writeFileRaw(filepath.Join(tx.dir, entry.Snapshot), data, nil); err != nil {
			return err
		}
	}
	tx.manifest.Entries = append(tx.manifest.Entries, entry)
	return tx.persist()
}

// backup starts a write-ahead transaction while Desktop is closed. It snapshots
// app auth and Code/project metadata, never conversation transcripts or workers.
// extraPaths may name spaces-seen.json next to the backups directory.
func backup(d desktop, root string, extraPaths ...string) (string, error) {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	auxAlias, err := filepath.Abs(filepath.Dir(root))
	if err != nil {
		return "", err
	}
	desktopRoot, err := canonicalDirectory(d.dir)
	if err != nil {
		return "", err
	}
	if _, active := backupRegistry.active[desktopRoot]; active {
		return "", errors.New("this Desktop already has an active recovery transaction")
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if pending, err := pendingBackupsUnlocked(d, root); err != nil {
		return "", err
	} else if len(pending) != 0 {
		return "", errors.New("an unfinished switch requires recovery before another switch")
	}
	dir, err := os.MkdirTemp(root, "tx-")
	if err != nil {
		return "", err
	}
	// O_EXCL also arbitrates distinct processes, independently of the in-memory
	// registry. A crash leaves the marker and manifest for deliberate recovery.
	marker := filepath.Join(root, ".pending-transaction")
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("another transaction owns the backup directory")
	}
	_, writeErr := f.WriteString(filepath.Base(dir))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := syncDirectory(root); err != nil {
		return "", err
	}
	alias, _ := filepath.Abs(d.dir)
	tx := &backupTransaction{dir: dir, desktopAlias: alias, auxAlias: auxAlias,
		manifest: backupManifest{Version: transactionVersion, DesktopRoot: desktopRoot,
			AuxRoot: filepath.Dir(root), Created: time.Now().UTC(), Phase: "preparing"}}
	if err := tx.persist(); err != nil {
		return "", err
	}
	var paths []string
	paths = append(paths, "config.json")
	for _, prefix := range []string{"", "Network"} {
		for _, name := range []string{"Cookies", "Cookies-journal", "Cookies-wal", "Cookies-shm"} {
			paths = append(paths, filepath.Join(prefix, name))
		}
	}
	sessions := filepath.Join(desktopRoot, "claude-code-sessions")
	err = filepath.WalkDir(sessions, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == sessions {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return coded("LINK_IN_CONVERSATIONS", "one of Claude's conversation folders contains a link to another file, which the helper will not copy; remove that link and try again")
		}
		if !entry.IsDir() {
			rel, _ := filepath.Rel(desktopRoot, path)
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	spaces, err := filepath.Glob(filepath.Join(desktopRoot, "local-agent-mode-sessions", "*", "*", "spaces.json"))
	if err != nil {
		return "", err
	}
	for _, path := range spaces {
		rel, _ := filepath.Rel(desktopRoot, path)
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if err := tx.addSnapshot("desktop", rel); err != nil {
			return "", err
		}
	}
	for _, extra := range extraPaths {
		parent, err := filepath.EvalSymlinks(filepath.Dir(extra))
		if err != nil || parent != tx.manifest.AuxRoot || filepath.Base(extra) != "spaces-seen.json" {
			return "", errors.New("unsupported auxiliary backup path")
		}
		if err := tx.addSnapshot("aux", "spaces-seen.json"); err != nil {
			return "", err
		}
	}
	tx.manifest.Phase = "prepared"
	if err := tx.persist(); err != nil {
		return "", err
	}
	backupRegistry.active[desktopRoot] = tx
	return dir, nil
}

func validateActiveTransactionPath(path string) error {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	for _, tx := range backupRegistry.active {
		_, _, _, err := tx.classify(path)
		if err != nil {
			return err
		}
	}
	return nil
}

func journalMutation(path string, after fileFingerprint, apply func() error) error {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	for _, tx := range backupRegistry.active {
		scope, rel, within, err := tx.classify(path)
		if err != nil {
			return err
		}
		if !within {
			continue
		}
		if tx.manifest.Phase != "prepared" && tx.manifest.Phase != "mutating" {
			return errors.New("transaction is sealed; additional metadata changes are refused")
		}
		var entry *backupEntry
		for i := range tx.manifest.Entries {
			if tx.manifest.Entries[i].Scope == scope && tx.manifest.Entries[i].Path == rel {
				entry = &tx.manifest.Entries[i]
				break
			}
		}
		if entry == nil {
			// Metadata created by this switch was absent from the initial scan.
			tx.manifest.Entries = append(tx.manifest.Entries, backupEntry{Scope: scope, Path: rel})
			entry = &tx.manifest.Entries[len(tx.manifest.Entries)-1]
		}
		current, err := fingerprintFile(path)
		if err != nil {
			return err
		}
		if !entry.matches(current) {
			return errors.New("metadata changed outside the switch; refusing to overwrite it")
		}
		if current == after {
			return nil
		}
		entry.Intents = append(entry.Intents, after)
		tx.manifest.Phase = "mutating"
		if err := tx.persist(); err != nil {
			return err
		}
		return apply()
	}
	return apply()
}

func (entry backupEntry) matches(current fileFingerprint) bool {
	if current == entry.Before {
		return true
	}
	if entry.Sealed != nil {
		return current == *entry.Sealed
	}
	for _, intent := range entry.Intents {
		if current == intent {
			return true
		}
	}
	return false
}

func loadBackup(d desktop, dir string) (*backupTransaction, error) {
	if _, err := canonicalDirectory(dir); err != nil {
		return nil, err
	}
	data, _, err := readStableFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest backupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, errors.New("invalid backup manifest")
	}
	switch manifest.Phase {
	case "preparing", "prepared", "mutating", "sealed", "restoring", "committed", "rolled-back":
	default:
		return nil, errors.New("unrecognized backup transaction phase")
	}
	root, err := canonicalDirectory(d.dir)
	if err != nil {
		return nil, err
	}
	auxRoot, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(dir)))
	if err != nil || manifest.Version != transactionVersion || manifest.DesktopRoot != root || manifest.AuxRoot != auxRoot {
		return nil, errors.New("backup does not belong to this Desktop and switcher directory")
	}
	alias, _ := filepath.Abs(d.dir)
	auxAlias, _ := filepath.Abs(filepath.Dir(filepath.Dir(dir)))
	tx := &backupTransaction{dir: dir, desktopAlias: alias, auxAlias: auxAlias, manifest: manifest}
	seen := map[string]bool{}
	for _, entry := range manifest.Entries {
		if _, err := tx.target(entry.Scope, entry.Path); err != nil {
			return nil, err
		}
		key := entry.Scope + "/" + entry.Path
		if seen[key] {
			return nil, errors.New("duplicate backup path")
		}
		seen[key] = true
		if entry.Before.Exists && (filepath.Dir(entry.Snapshot) != "files" || filepath.Base(entry.Snapshot) == "." || len(filepath.Base(entry.Snapshot)) != 6) {
			return nil, errors.New("invalid backup snapshot path")
		}
	}
	return tx, nil
}

// sealBackup freezes the exact bytes written by the switch before Desktop is
// reopened. It also works after a failed write: the journal was durable first.
func sealBackup(d desktop, dir string) error {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	tx, err := loadBackup(d, dir)
	if err != nil {
		return err
	}
	if tx.manifest.Phase == "committed" || tx.manifest.Phase == "rolled-back" {
		return errors.New("transaction is already finished")
	}
	for i := range tx.manifest.Entries {
		entry := &tx.manifest.Entries[i]
		if len(entry.Intents) == 0 {
			continue
		}
		path, _ := tx.target(entry.Scope, entry.Path)
		current, err := fingerprintFile(path)
		if err != nil {
			return err
		}
		if !entry.matches(current) {
			return errors.New("metadata changed outside the switch; cannot seal transaction")
		}
		entry.Sealed = &current
	}
	tx.manifest.Phase = "sealed"
	if err := tx.persist(); err != nil {
		return err
	}
	backupRegistry.active[tx.manifest.DesktopRoot] = tx
	return nil
}

func releaseTransaction(tx *backupTransaction) error {
	delete(backupRegistry.active, tx.manifest.DesktopRoot)
	marker := filepath.Join(filepath.Dir(tx.dir), ".pending-transaction")
	data, _, err := readStableFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(data) != filepath.Base(tx.dir) {
		return errors.New("another transaction owns the recovery marker")
	}
	return removeFileRaw(marker)
}

// settleBackup closes a transaction that can no longer be rolled back because
// Claude is running again and has rewritten its own files. Written files that
// still hold exactly what the switch wrote are sealed as usual; the others are
// left as Claude has them. The transaction is then marked committed and its
// pending marker released, so it cannot block the next switch. It writes
// nothing to Claude's files, and the snapshots stay until they are pruned.
func settleBackup(d desktop, dir string) error {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	tx, err := loadBackup(d, dir)
	if err != nil {
		return err
	}
	if tx.manifest.Phase == "committed" || tx.manifest.Phase == "rolled-back" {
		return releaseTransaction(tx)
	}
	for i := range tx.manifest.Entries {
		entry := &tx.manifest.Entries[i]
		if len(entry.Intents) == 0 || entry.Sealed != nil {
			continue
		}
		path, err := tx.target(entry.Scope, entry.Path)
		if err != nil {
			continue
		}
		if current, err := fingerprintFile(path); err == nil && entry.matches(current) {
			entry.Sealed = &current
		}
	}
	tx.manifest.Phase = "committed"
	if err := tx.persist(); err != nil {
		return err
	}
	return releaseTransaction(tx)
}

// restoreBackup must be called with Desktop closed. It preflights all touched
// files before changing any, then rechecks each one. A mismatch preserves every
// newer file and leaves the recovery journal pending for human review.
func restoreBackup(d desktop, dir string) error {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	tx, err := loadBackup(d, dir)
	if err != nil {
		return err
	}
	if tx.manifest.Phase == "committed" {
		return errors.New("committed transaction cannot be automatically rolled back")
	}
	if tx.manifest.Phase == "rolled-back" {
		return releaseTransaction(tx)
	}
	for _, entry := range tx.manifest.Entries {
		if len(entry.Intents) == 0 {
			continue
		}
		path, _ := tx.target(entry.Scope, entry.Path)
		current, err := fingerprintFile(path)
		if err != nil {
			return err
		}
		if !entry.matches(current) {
			return errors.New("rollback blocked: a touched file contains newer changes")
		}
		if _, err := tx.snapshotBytes(entry); err != nil {
			return err
		}
	}
	tx.manifest.Phase = "restoring"
	if err := tx.persist(); err != nil {
		return err
	}
	// The order matters. Claude treats one account's tokens next to another
	// account's cookies as an account takeover, and a put-back can be cut
	// short like anything else. So the sign-in settings are taken out of the
	// settings file first, then the cookie files go back, then everything
	// else, and the settings file itself last. Whatever is on disk in between
	// is one account or nobody.
	var cookies, others []int
	settings := -1
	cookiesOwed := false
	for i := len(tx.manifest.Entries) - 1; i >= 0; i-- {
		entry := tx.manifest.Entries[i]
		switch {
		case entry.isSettingsFile():
			settings = i
		case entry.isCookieFile():
			cookies = append(cookies, i)
			if len(entry.Intents) > 0 {
				path, _ := tx.target(entry.Scope, entry.Path)
				if current, err := fingerprintFile(path); err != nil || current != entry.Before {
					cookiesOwed = true
				}
			}
		default:
			others = append(others, i)
		}
	}
	if cookiesOwed && settings >= 0 {
		if err := tx.removeSignInSettings(settings); err != nil {
			return err
		}
	}
	order := append(append(cookies, others...), settings)
	for _, i := range order {
		if i < 0 {
			continue
		}
		if err := tx.restoreEntry(i); err != nil {
			return err
		}
	}
	tx.manifest.Phase = "rolled-back"
	if err := tx.persist(); err != nil {
		return err
	}
	return releaseTransaction(tx)
}

func (entry backupEntry) isSettingsFile() bool {
	return entry.Scope == "desktop" && entry.Path == "config.json"
}

func (entry backupEntry) isCookieFile() bool {
	return entry.Scope == "desktop" && strings.HasPrefix(filepath.Base(entry.Path), "Cookies")
}

// snapshotBytes reads what a file held before the switch, checked against
// the fingerprint taken then. It is nil for a file that did not exist.
func (tx *backupTransaction) snapshotBytes(entry backupEntry) ([]byte, error) {
	if !entry.Before.Exists {
		return nil, nil
	}
	snapshotPath, err := safeRelative(tx.dir, entry.Snapshot)
	if err != nil {
		return nil, err
	}
	data, state, err := readStableFile(snapshotPath)
	if err != nil {
		return nil, err
	}
	if state.SHA256 != entry.Before.SHA256 || state.Size != entry.Before.Size {
		return nil, errors.New("rollback blocked: snapshot integrity check failed")
	}
	return data, nil
}

// restoreEntry puts one file back as it was before the switch. A file the
// switch never touched, or one that is already back, is left alone.
func (tx *backupTransaction) restoreEntry(i int) error {
	entry := tx.manifest.Entries[i]
	if len(entry.Intents) == 0 {
		return nil
	}
	path, err := tx.target(entry.Scope, entry.Path)
	if err != nil {
		return err
	}
	current, err := fingerprintFile(path)
	if err != nil {
		return err
	}
	if current == entry.Before {
		return nil
	}
	if !entry.matches(current) {
		return errors.New("rollback stopped: a touched file changed during recovery")
	}
	if !entry.Before.Exists {
		return removeFileRaw(path)
	}
	data, err := tx.snapshotBytes(entry)
	if err != nil {
		return err
	}
	return writeFileRaw(path, data, &entry.Before)
}

// removeSignInSettings takes the sign-in entries out of Claude's settings
// file as the first step of a put-back. The file it leaves is recorded in the
// journal before it is written, so a put-back that is cut short here can be
// resumed.
func (tx *backupTransaction) removeSignInSettings(i int) error {
	entry := &tx.manifest.Entries[i]
	path, err := tx.target(entry.Scope, entry.Path)
	if err != nil {
		return err
	}
	current, err := fingerprintFile(path)
	if err != nil {
		return err
	}
	if !current.Exists {
		return nil
	}
	if !entry.matches(current) {
		return errors.New("rollback stopped: a touched file changed during recovery")
	}
	return tx.removeSignInSettingsFrom(entry, path)
}

func (tx *backupTransaction) removeSignInSettingsFrom(entry *backupEntry, path string) error {
	cfg, err := readObject(path)
	if err != nil {
		return err
	}
	found := false
	for _, key := range authKeys {
		if _, ok := cfg.get(key); ok {
			found = true
			cfg.del(key)
		}
	}
	if !found {
		return nil
	}
	data, err := cfg.bytes()
	if err != nil {
		return err
	}
	tmp, after, err := stageProtectedFile(path, data, nil)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	entry.Intents = append(entry.Intents, after)
	if err := tx.persist(); err != nil {
		return err
	}
	if err := rejectLinkedFile(path); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func pendingBackups(d desktop, root string) ([]string, error) {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	return pendingBackupsUnlocked(d, root)
}

func pendingBackupsUnlocked(d desktop, root string) ([]string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	root = canonical
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "tx-") {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("invalid transaction directory")
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Lstat(filepath.Join(dir, "manifest.json")); errors.Is(err, os.ErrNotExist) {
			// A pre-journal crash cannot have changed app files. Keep the
			// incomplete directory; a marker targeting it still blocks below.
			continue
		}
		tx, err := loadBackup(d, dir)
		if err != nil {
			// A backup that can no longer be read (the home folder was renamed,
			// the data came from another Mac) is not an interrupted switch.
			// Only the marker below can say that one is still owed.
			continue
		}
		if tx.manifest.Phase != "committed" && tx.manifest.Phase != "rolled-back" {
			pending = append(pending, dir)
		}
	}
	marker, _, err := readStableFile(filepath.Join(root, ".pending-transaction"))
	if err == nil {
		name := string(marker)
		if filepath.Base(name) != name || !strings.HasPrefix(name, "tx-") {
			return nil, errors.New("invalid pending transaction marker")
		}
		tx, err := loadBackup(d, filepath.Join(root, name))
		if err != nil {
			if _, statErr := os.Lstat(filepath.Join(root, name, "manifest.json")); errors.Is(statErr, os.ErrNotExist) {
				// No live writes are possible until a prepared journal exists.
				// Keep the partial snapshot, but release its pre-journal marker.
				if err := removeFileRaw(filepath.Join(root, ".pending-transaction")); err != nil {
					return nil, err
				}
				return pending, nil
			}
			return nil, errors.New("unfinished snapshot preparation requires recovery review")
		}
		if tx.manifest.Phase == "committed" || tx.manifest.Phase == "rolled-back" {
			// Recover a crash after the terminal manifest was durable but
			// before its marker was removed. No app data is changed.
			if err := releaseTransaction(tx); err != nil {
				return nil, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Strings(pending)
	return pending, nil
}

const (
	// Finished backups are kept for this long, and the newest few are kept
	// however old they are.
	backupKeepFor    = 14 * 24 * time.Hour
	backupKeepNewest = 10
	prunedPrefix     = ".pruned-"
)

// pruneBackups removes finished backups that are both older than
// backupKeepFor and not among the newest backupKeepNewest. A backup holds
// saved sign-ins, so it is deleted, never moved to the Trash. Each one is
// first renamed out of the way in one step, so a clean-up that is cut short
// never leaves half a backup under its own name. An unfinished backup, a
// folder without a manifest and anything that is not a backup are left alone.
func pruneBackups(d desktop, root string, now time.Time) (int, error) {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	type finished struct {
		dir     string
		created time.Time
	}
	var done []finished
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prunedPrefix) {
			_ = os.RemoveAll(filepath.Join(root, name))
			continue
		}
		if !strings.HasPrefix(name, "tx-") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(root, name)
		tx, err := loadBackup(d, dir)
		if err != nil || (tx.manifest.Phase != "committed" && tx.manifest.Phase != "rolled-back") {
			continue
		}
		done = append(done, finished{dir, tx.manifest.Created})
	}
	if marker, _, err := readStableFile(filepath.Join(root, ".pending-transaction")); err == nil {
		// A marker still names a backup: nothing it points at is removed.
		kept := done[:0]
		for _, backup := range done {
			if filepath.Base(backup.dir) != string(marker) {
				kept = append(kept, backup)
			}
		}
		done = kept
	}
	sort.Slice(done, func(i, j int) bool { return done[i].created.After(done[j].created) })
	removed := 0
	var firstErr error
	for i, backup := range done {
		if i < backupKeepNewest || now.Sub(backup.created) <= backupKeepFor {
			continue
		}
		aside := filepath.Join(root, prunedPrefix+filepath.Base(backup.dir))
		if err := os.Rename(backup.dir, aside); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
		if err := os.RemoveAll(aside); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return removed, firstErr
}

// backupPhase is how far a backup got ("" when it cannot be read).
func backupPhase(d desktop, dir string) string {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	tx, err := loadBackup(d, dir)
	if err != nil {
		return ""
	}
	return tx.manifest.Phase
}

// setAsideUnreadableBackup deals with a marker that names a backup which can
// no longer be read: nothing can be put back from it, and it must not refuse
// every later switch. The folder is kept under another name, the marker is
// removed. It does nothing when the marked backup can be read.
func setAsideUnreadableBackup(d desktop, root string) error {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	markerPath := filepath.Join(canonical, ".pending-transaction")
	marker, _, err := readStableFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	name := string(marker)
	if filepath.Base(name) != name || !strings.HasPrefix(name, "tx-") {
		// The marker itself is damaged: it names nothing that could be put back.
		return removeFileRaw(markerPath)
	}
	dir := filepath.Join(canonical, name)
	if _, err := loadBackup(d, dir); err == nil {
		return errors.New("the marked backup can be read; it is not set aside")
	}
	for key, tx := range backupRegistry.active {
		if tx.dir == dir {
			delete(backupRegistry.active, key)
		}
	}
	if _, err := os.Lstat(dir); err == nil {
		aside := filepath.Join(canonical, ".unreadable-"+name)
		if err := os.RemoveAll(aside); err != nil {
			return err
		}
		if err := os.Rename(dir, aside); err != nil {
			return err
		}
	}
	return removeFileRaw(markerPath)
}
