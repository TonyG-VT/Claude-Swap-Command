package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transactionFixture(t *testing.T) (desktop, string) {
	t.Helper()
	base := t.TempDir()
	d := desktop{dir: filepath.Join(base, "Desktop")}
	write(t, d.configPath(), sampleConfig)
	root := filepath.Join(base, "Switcher", "backups")
	t.Cleanup(func() {
		backupRegistry.Lock()
		defer backupRegistry.Unlock()
		canonicalBase, _ := filepath.EvalSymlinks(base)
		for key, tx := range backupRegistry.active {
			if strings.HasPrefix(tx.dir, canonicalBase+string(filepath.Separator)) {
				delete(backupRegistry.active, key)
			}
		}
	})
	return d, root
}

func mustBackup(t *testing.T, d desktop, root string, extra ...string) string {
	t.Helper()
	dir, err := backup(d, root, extra...)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustWriteProtected(t *testing.T, path, data string) {
	t.Helper()
	if err := writeFileAtomic(path, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func forgetTransactionsForTest() {
	backupRegistry.Lock()
	defer backupRegistry.Unlock()
	backupRegistry.active = make(map[string]*backupTransaction)
}

func TestTransactionRestoresOnlyTouchedMetadata(t *testing.T) {
	d, root := transactionFixture(t)
	oldSession := filepath.Join(d.sessionsDir(), "a", "org", "local_one.json")
	newSession := filepath.Join(d.sessionsDir(), "b", "org", "local_one.json")
	untouched := filepath.Join(d.sessionsDir(), "a", "org", "local_two.json")
	space := filepath.Join(d.spacesDir(), "b", "org", "spaces.json")
	seen := filepath.Join(filepath.Dir(root), "spaces-seen.json")
	write(t, oldSession, `{"lastActivityAt":1}`)
	write(t, untouched, `{"lastActivityAt":2}`)
	write(t, space, `{"spaces":[]}`)
	write(t, seen, `{"b/org":[]}`)
	write(t, d.cookieFiles()[0], "fixture-cookie-a")
	write(t, d.cookieFiles()[2], "fixture-wal-a")
	// Cowork conversation contents are deliberately outside the snapshot scope.
	write(t, filepath.Join(d.spacesDir(), "b", "org", "transcript.jsonl"), "must not be backed up")
	bk := mustBackup(t, d, root, seen)
	if err := clearSignIn(d); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(oldSession, newSession); err != nil {
		t.Fatal(err)
	}
	mustWriteProtected(t, space, `{"spaces":[{"id":"new"}]}`)
	mustWriteProtected(t, seen, `{"b/org":["new"]}`)
	if err := sealBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	// An unrelated app edit after the seal must survive rollback.
	write(t, untouched, `{"lastActivityAt":999}`)
	if err := restoreBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if got := read(t, d.configPath()); got != sampleConfig {
		t.Fatal("account config was not restored")
	}
	if read(t, d.cookieFiles()[0]) != "fixture-cookie-a" || read(t, d.cookieFiles()[2]) != "fixture-wal-a" {
		t.Fatal("cookie store or SQLite sidecar was not restored")
	}
	if _, err := os.Stat(newSession); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("transaction-created session entry survived rollback")
	}
	if read(t, space) != `{"spaces":[]}` || read(t, seen) != `{"b/org":[]}` {
		t.Fatal("project metadata was not restored")
	}
	if read(t, untouched) != `{"lastActivityAt":999}` {
		t.Fatal("untouched newer metadata was overwritten")
	}
	tx, err := loadBackup(d, bk)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range tx.manifest.Entries {
		if strings.Contains(entry.Path, "transcript") {
			t.Fatal("conversation transcript entered the backup")
		}
	}
	if paths, err := pendingBackups(d, root); err != nil || len(paths) != 0 {
		t.Fatalf("rollback left a pending transaction: %v", err)
	}
}

func TestTransactionRollbackRefusesNewerTouchedFileBeforeAnyRestore(t *testing.T) {
	d, root := transactionFixture(t)
	session := filepath.Join(d.sessionsDir(), "a", "org", "local_one.json")
	write(t, session, "before")
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"account":"switched"}`)
	mustWriteProtected(t, session, "ours")
	if err := sealBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	write(t, session, "new user work")
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("rollback overwrote newer metadata")
	}
	if read(t, session) != "new user work" || read(t, d.configPath()) != `{"account":"switched"}` {
		t.Fatal("rollback changed files despite a preflight conflict")
	}
}

func TestTransactionRefusesEvenIdenticalLaterRewrite(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"new":true}`)
	if err := sealBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Minute)
	if err := os.Chtimes(d.configPath(), at, at); err != nil {
		t.Fatal(err)
	}
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("rollback ignored evidence of a later write")
	}
}

func TestTransactionCrashBeforeSealCanRecoverFromJournal(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"after":true}`)
	forgetTransactionsForTest() // emulate a process exit, retaining only files.
	paths, err := pendingBackups(d, root)
	if err != nil || len(paths) != 1 || paths[0] != bk {
		t.Fatalf("pending journal was not discovered: %v", err)
	}
	if _, err := backup(d, root); err == nil {
		t.Fatal("new switch began while crash recovery was pending")
	}
	if err := restoreBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if read(t, d.configPath()) != sampleConfig {
		t.Fatal("crash recovery did not restore original config")
	}
}

func TestTransactionIntentWithoutAppliedWriteIsRecoverable(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	staged, state, err := stageProtectedFile(d.configPath(), []byte(`{"after":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(staged)
	if err := journalMutation(d.configPath(), state, func() error { return errors.New("simulated rename failure") }); err == nil {
		t.Fatal("simulated failure not returned")
	}
	forgetTransactionsForTest()
	if err := restoreBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if read(t, d.configPath()) != sampleConfig {
		t.Fatal("unapplied intent altered original state")
	}
}

func TestTransactionMultipleWritesThenFailedIntentRecoverOriginal(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"intermediate":true}`)
	staged, state, err := stageProtectedFile(d.configPath(), []byte(`{"unapplied":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(staged)
	_ = journalMutation(d.configPath(), state, func() error { return errors.New("simulated crash boundary") })
	forgetTransactionsForTest()
	if err := restoreBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if read(t, d.configPath()) != sampleConfig {
		t.Fatal("previous applied intent was lost when a later intent failed")
	}
}

func TestTransactionRejectsExternalCreationAfterSnapshot(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	newSession := filepath.Join(d.sessionsDir(), "new", "org", "local_one.json")
	write(t, newSession, "external new work")
	if err := writeFileAtomic(newSession, []byte("switcher work")); err == nil {
		t.Fatal("switch overwrote a file created after its snapshot")
	}
	if err := restoreBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if read(t, newSession) != "external new work" {
		t.Fatal("recovery removed externally created metadata")
	}
}

func TestTransactionPreJournalCrashLeavesAppUntouched(t *testing.T) {
	d, root := transactionFixture(t)
	dir := filepath.Join(root, "tx-interrupted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".pending-transaction"), "tx-interrupted")
	if pending, err := pendingBackups(d, root); err != nil || len(pending) != 0 {
		t.Fatalf("pre-journal preparation was not safely released: %v", err)
	}
	if read(t, d.configPath()) != sampleConfig {
		t.Fatal("pre-journal recovery modified app state")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("incomplete snapshot directory was deleted")
	}
}

func TestTransactionSnapshotIntegrityFailureIsNonDestructive(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"after":true}`)
	tx, err := loadBackup(d, bk)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range tx.manifest.Entries {
		if entry.Path == "config.json" {
			write(t, filepath.Join(bk, entry.Snapshot), "corrupt fixture")
		}
	}
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("corrupt snapshot was restored")
	}
	if read(t, d.configPath()) != `{"after":true}` {
		t.Fatal("failed integrity check changed live config")
	}
}

func TestTransactionCommitRetainsBackupAndClosesRecovery(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"after":true}`)
	if err := sealBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(d.configPath(), []byte(`{"unexpected":true}`)); err == nil {
		t.Fatal("sealed transaction accepted another switcher mutation")
	}
	if err := settleBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bk, "manifest.json")); err != nil {
		t.Fatal("committed backup was deleted")
	}
	if pending, err := pendingBackups(d, root); err != nil || len(pending) != 0 {
		t.Fatalf("committed backup stayed pending: %v", err)
	}
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("automatic rollback accepted a committed backup")
	}
}

func TestTransactionRejectsLinkedMetadataAndEscapingManifest(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	outside := t.TempDir()
	if err := os.MkdirAll(d.sessionsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d.sessionsDir(), "linked")); err != nil {
		t.Skip("fixture symlink unavailable")
	}
	if err := writeFileAtomic(filepath.Join(d.sessionsDir(), "linked", "org", "local_one.json"), []byte("bad")); err == nil {
		t.Fatal("write followed a session directory symlink")
	}
	tx, err := loadBackup(d, bk)
	if err != nil {
		t.Fatal(err)
	}
	tx.manifest.Entries[0].Path = "../outside"
	data, _ := json.Marshal(tx.manifest)
	write(t, filepath.Join(bk, "manifest.json"), string(data))
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("rollback accepted a path outside its scope")
	}
}

func TestProfilePrivacyAndIncompleteSave(t *testing.T) {
	d, root := transactionFixture(t)
	dir := filepath.Join(filepath.Dir(root), "accounts", "a")
	write(t, d.cookieFiles()[0], "fixture-cookie")
	if err := saveProfile(d, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "Cookies", "profile-manifest.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("profile file is not private: %s", name)
		}
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatal("profile directory is not private")
	}
	write(t, filepath.Join(dir, ".save-pending"), "interrupted fixture")
	if err := restoreProfileSafely(d, dir, func() bool { return true }); err == nil {
		t.Fatal("partially saved profile was accepted")
	}
	if read(t, d.configPath()) != sampleConfig {
		t.Fatal("refused profile altered the app")
	}
}

func TestProfileRejectsTamperingBeforeAppMutation(t *testing.T) {
	d, root := transactionFixture(t)
	dir := filepath.Join(filepath.Dir(root), "accounts", "a")
	write(t, d.cookieFiles()[0], "fixture-cookie")
	if err := saveProfile(d, dir); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "Cookies"), "corrupt fixture")
	if err := restoreProfileSafely(d, dir, func() bool { return true }); err == nil {
		t.Fatal("tampered profile was accepted")
	}
	if read(t, d.cookieFiles()[0]) != "fixture-cookie" {
		t.Fatal("app cookies changed despite invalid profile")
	}
}
