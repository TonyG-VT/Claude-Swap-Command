package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// agedBackup makes one finished backup and dates it.
func agedBackup(t *testing.T, d desktop, root string, created time.Time, finish bool) string {
	t.Helper()
	bk := mustBackup(t, d, root)
	if err := sealBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	if finish {
		if err := settleBackup(d, bk); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(bk, "manifest.json")
	var manifest map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["created"] = created.UTC().Format(time.RFC3339Nano)
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return bk
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestPruneKeepsRecentBackupsAndTheNewestTen(t *testing.T) {
	d, root := transactionFixture(t)
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	// Thirteen old backups, 20 to 32 days old: only the newest ten of all stay.
	var old []string
	for i := 0; i < 13; i++ {
		old = append(old, agedBackup(t, d, root, now.Add(-time.Duration(20+i)*day), true))
	}
	removed, err := pruneBackups(d, root, now)
	if err != nil || removed != 3 {
		t.Fatalf("removed %d, %v; want the 3 oldest gone", removed, err)
	}
	for i, bk := range old {
		if exists(bk) != (i < 10) {
			t.Fatalf("backup %d (%d days old): kept=%v", i, 20+i, exists(bk))
		}
	}
	// Twelve recent ones on top: everything younger than 14 days stays, however many.
	var recent []string
	for i := 0; i < 12; i++ {
		recent = append(recent, agedBackup(t, d, root, now.Add(-time.Duration(i)*time.Hour), true))
	}
	removed, err = pruneBackups(d, root, now)
	if err != nil || removed != 10 {
		t.Fatalf("removed %d, %v; want the 10 old ones gone", removed, err)
	}
	for _, bk := range recent {
		if !exists(bk) {
			t.Fatal("a backup younger than 14 days must stay")
		}
	}
	for _, bk := range old {
		if exists(bk) {
			t.Fatal("an old backup beyond the newest ten must go")
		}
	}
	if again, err := pruneBackups(d, root, now); err != nil || again != 0 {
		t.Fatalf("a second pass has nothing to do: %d %v", again, err)
	}
}

func TestPruneNeverTouchesAnUnfinishedBackupOrAnythingElse(t *testing.T) {
	d, root := transactionFixture(t)
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for i := 0; i < 11; i++ {
		agedBackup(t, d, root, now.Add(-time.Duration(30+i)*day), true)
	}
	// The newest by far, but never finished: it is what "Fix an interrupted switch…" needs.
	unfinished := agedBackup(t, d, root, now.Add(-90*day), false)
	// Things that are not backups.
	stray := filepath.Join(root, "notes.txt")
	write(t, stray, "keep")
	half := filepath.Join(root, "tx-half")
	if err := os.Mkdir(half, 0o700); err != nil {
		t.Fatal(err)
	}
	// A clean-up that was cut short earlier.
	leftover := filepath.Join(root, ".pruned-tx-leftover")
	write(t, filepath.Join(leftover, "files", "000001"), "x")

	removed, err := pruneBackups(d, root, now)
	if err != nil || removed != 1 {
		t.Fatalf("removed %d, %v; want exactly the oldest finished one gone", removed, err)
	}
	if !exists(unfinished) || !exists(stray) || !exists(half) {
		t.Fatal("an unfinished backup, an incomplete folder and unrelated files are never touched")
	}
	if exists(leftover) {
		t.Fatal("what an earlier, interrupted clean-up left behind is removed")
	}
	if pending, err := pendingBackups(d, root); err != nil || len(pending) != 1 || pending[0] != unfinished {
		t.Fatalf("the unfinished backup is still pending: %v %v", pending, err)
	}
}
