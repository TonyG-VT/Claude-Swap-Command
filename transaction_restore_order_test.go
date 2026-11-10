package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func forgetTransactionsUnder(t *testing.T, base string) {
	t.Cleanup(func() {
		backupRegistry.Lock()
		defer backupRegistry.Unlock()
		canonical, _ := filepath.EvalSymlinks(base)
		for key, tx := range backupRegistry.active {
			if strings.HasPrefix(tx.dir, canonical+string(filepath.Separator)) {
				delete(backupRegistry.active, key)
			}
		}
	})
}

// lockFolder makes a folder refuse new files until the test ends or unlock is
// called: the way a put-back is cut short here.
func lockFolder(t *testing.T, dir string) (unlock func()) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	unlock = func() { _ = os.Chmod(dir, 0o700) }
	t.Cleanup(unlock)
	return unlock
}

// A put-back can be cut short too (the disk fills up, the Mac loses power).
// It must then leave nobody's tokens on disk, never account A's tokens next to
// account B's cookies: Claude treats that as an account takeover.
func TestPutBackNeverLeavesOneAccountsTokensWithAnothersCookies(t *testing.T) {
	root := t.TempDir()
	forgetTransactionsUnder(t, root)
	d := desktop{dir: filepath.Join(root, "claude")}
	other := desktop{dir: filepath.Join(root, "claude-b")}
	// The layout that keeps cookies in their own folder, so the put-back can
	// be stopped exactly between the settings file and the cookies.
	for _, each := range []desktop{d, other} {
		if err := os.MkdirAll(filepath.Join(each.dir, "Network"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, other.configPath(), `{"lastKnownAccountUuid":"b","oauth:tokenCacheV2":"fixture-b"}`)
	write(t, other.cookieFiles()[0], "cookies-b")
	saved := filepath.Join(root, "accounts", "b")
	if err := saveProfile(other, saved); err != nil {
		t.Fatal(err)
	}
	write(t, d.configPath(), `{"locale":"en-US","lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-a"}`)
	write(t, d.cookieFiles()[0], "cookies-a")
	backups := filepath.Join(root, "helper", "backups")
	bk := mustBackup(t, d, backups)

	// The switch gets as far as account B being fully in place…
	if err := restoreProfileSafely(d, saved, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if got := signInOnDisk(t, d); got != "keys:b cookies:b" {
		t.Fatalf("fixture: %s", got)
	}
	// …and then has to be undone, but the put-back cannot write the cookies.
	unlock := lockFolder(t, filepath.Join(d.dir, "Network"))
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("fixture: the put-back must be cut short")
	}
	if got := signInOnDisk(t, d); got != "keys:none cookies:b" {
		t.Fatalf("a put-back that stops partway leaves nobody's tokens on disk, got %s", got)
	}
	if pending, err := pendingBackups(d, backups); err != nil || len(pending) != 1 {
		t.Fatalf("the put-back is still owed: %v %v", pending, err)
	}
	// Once the obstacle is gone, the same put-back finishes.
	unlock()
	if err := restoreBackup(d, bk); err != nil {
		t.Fatalf("resuming the put-back: %v", err)
	}
	if got := signInOnDisk(t, d); got != "keys:a cookies:a" {
		t.Fatalf("everything is back as it was, got %s", got)
	}
	if got := read(t, d.configPath()); got != `{"locale":"en-US","lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-a"}` {
		t.Fatalf("the settings file is back byte for byte: %s", got)
	}
	if pending, err := pendingBackups(d, backups); err != nil || len(pending) != 0 {
		t.Fatalf("nothing is owed any more: %v %v", pending, err)
	}
}

// One backup folder that can no longer be read (the home folder was renamed,
// the data came from another Mac) must not stop every later switch.
func TestAFinishedBackupThatCannotBeReadDoesNotBlockAnything(t *testing.T) {
	d, root := transactionFixture(t)
	old := mustBackup(t, d, root)
	if err := sealBackup(d, old); err != nil {
		t.Fatal(err)
	}
	if err := settleBackup(d, old); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(old, "manifest.json")
	moved := strings.Replace(read(t, manifest), `"desktopRoot": "`, `"desktopRoot": "/somewhere/else`, 1)
	if moved == read(t, manifest) {
		t.Fatal("fixture: the manifest was not changed")
	}
	if err := os.WriteFile(manifest, []byte(moved), 0o600); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingBackups(d, root); err != nil || len(pending) != 0 {
		t.Fatalf("an old backup that cannot be read is not an interrupted switch: %v %v", pending, err)
	}
	next := mustBackup(t, d, root)
	if err := sealBackup(d, next); err != nil {
		t.Fatal(err)
	}
	if err := settleBackup(d, next); err != nil {
		t.Fatal(err)
	}
	if !exists(old) {
		t.Fatal("the unreadable folder is left where it is")
	}
}

func TestALinkInAConversationFolderIsRefusedInPlainWords(t *testing.T) {
	d, root := transactionFixture(t)
	folder := filepath.Join(d.sessionsDir(), carryID(1), carryID(2))
	write(t, filepath.Join(folder, "local_"+carryID(3)+".json"), "{}")
	if err := os.Symlink(filepath.Join(folder, "local_"+carryID(3)+".json"), filepath.Join(folder, "linked.json")); err != nil {
		t.Fatal(err)
	}
	_, err := backup(d, root)
	if errorCode(err) != "LINK_IN_CONVERSATIONS" || !strings.Contains(sanitizeError(err), "a link") {
		t.Fatalf("the refusal names the reason: [%s] %s", errorCode(err), sanitizeError(err))
	}
}
