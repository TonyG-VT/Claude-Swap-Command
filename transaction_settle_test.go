package main

import (
	"os"
	"testing"
)

// Once Claude is running again it rewrites its own files, so a switch can no
// longer be rolled back or even sealed. Such a transaction must still be
// closed: left open, its marker would refuse every later switch.
func TestSettleClosesATransactionClaudeHasAlreadyWrittenOver(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"lastKnownAccountUuid":"b"}`)
	// Claude starts and rewrites its settings file.
	if err := os.WriteFile(d.configPath(), []byte(`{"lastKnownAccountUuid":"b","updaterRunningInstance":{"pid":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sealBackup(d, bk); err == nil {
		t.Fatal("fixture: sealing must fail once the file changed outside the switch")
	}
	if pending, err := pendingBackups(d, root); err != nil || len(pending) != 1 {
		t.Fatalf("fixture: the transaction is still pending: %v %v", pending, err)
	}

	if err := settleBackup(d, bk); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if pending, err := pendingBackups(d, root); err != nil || len(pending) != 0 {
		t.Fatalf("a settled transaction is no longer pending: %v %v", pending, err)
	}
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("a settled transaction is never rolled back automatically")
	}
	if got := read(t, d.configPath()); got != `{"lastKnownAccountUuid":"b","updaterRunningInstance":{"pid":1}}` {
		t.Fatalf("settling writes nothing to Claude's files: %s", got)
	}
	if err := settleBackup(d, bk); err != nil {
		t.Fatalf("settling twice is harmless: %v", err)
	}
	// The next switch can start.
	next := mustBackup(t, d, root)
	if err := sealBackup(d, next); err != nil {
		t.Fatal(err)
	}
	if err := settleBackup(d, next); err != nil {
		t.Fatal(err)
	}
}

func TestSettleKeepsTheNormalSealWhenNothingChanged(t *testing.T) {
	d, root := transactionFixture(t)
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"lastKnownAccountUuid":"b"}`)
	if err := settleBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	tx, err := loadBackup(d, bk)
	if err != nil || tx.manifest.Phase != "committed" {
		t.Fatalf("phase after settling: %v %v", tx.manifest.Phase, err)
	}
	sealed := false
	for _, entry := range tx.manifest.Entries {
		sealed = sealed || entry.Sealed != nil
	}
	if !sealed {
		t.Fatal("an unchanged written file is still sealed with its exact bytes")
	}
}
