package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A rolled-back switch used to say only "switch failed" with a stage code.
// It must now name the cause and its code, and still never carry raw text
// from a library error (paths, identifiers, file contents).
func TestTransactionFailuresKeepCodeAndCauseWithoutRawText(t *testing.T) {
	// Deliberately synthetic private-looking text, never real sign-in material.
	private := "fixture-private-value at /private/fixture/account.json"
	for _, tc := range []struct {
		name                 string
		change, active, seal error
		wantCode, wantText   string
	}{
		{"our own cause", coded("SAVED_SIGNIN_MISSING", "the saved sign-in for that account is missing"), nil, nil, "SAVED_SIGNIN_MISSING", "the saved sign-in for that account is missing"},
		{"our cause inside a plain wrapper", fmt.Errorf("%s: %w", private, coded("CARRY_APPLY_FAILED", "a conversation could not be copied")), nil, nil, "CARRY_APPLY_FAILED", "a conversation could not be copied"},
		{"raw library error", errors.New(private), nil, nil, "ROLLED_BACK", "an unexpected problem"},
		{"identity read", nil, errors.New(private), nil, "IDENTITY_UNAVAILABLE", "could not be read"},
		{"seal", nil, nil, errors.New(private), "SEAL_FAILED", "backup record could not be finished"},
		{"change and seal", coded("CARRY_APPLY_FAILED", "a conversation could not be copied"), nil, errors.New(private), "CARRY_APPLY_FAILED", "a conversation could not be copied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes, seals, restores, starts := 0, 0, 0, 0
			op := transactionOps{
				Prepare: func() error { return nil }, Quit: func() error { return nil },
				Closed: func() bool { return true }, Backup: func() (string, error) { return "fixture-backup", nil },
				Active:  func() (string, error) { return "fixture-account", tc.active },
				Seal:    func(string) error { seals++; return tc.seal },
				Restore: func(string) error { restores++; return nil },
				Start:   func() error { starts++; return nil },
			}
			backup, err := runDesktopTransaction(op, func(string) error { changes++; return tc.change })
			if backup != "fixture-backup" || err == nil {
				t.Fatalf("unexpected recovery result: backup=%q error=%v", backup, err)
			}
			if errorCode(err) != tc.wantCode {
				t.Fatalf("code = %s, want %s (%v)", errorCode(err), tc.wantCode, err)
			}
			if !strings.Contains(err.Error(), "everything was put back") || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("the text must state the outcome and the cause: %v", err)
			}
			if strings.Contains(err.Error(), "fixture-private") || strings.Contains(err.Error(), "/private/") || strings.Contains(err.Error(), "fixture-account") {
				t.Fatalf("private transaction detail escaped: %v", err)
			}
			wantChanges := 1
			if tc.active != nil {
				wantChanges = 0
			}
			if changes != wantChanges || seals != 1 || restores != 1 || starts != 1 {
				t.Fatal("diagnostics changed transaction or rollback execution")
			}
		})
	}
}

func TestTransactionDiagnosticsDoNotChangeRecoveryStops(t *testing.T) {
	cause := coded("CARRY_PLAN_FAILED", "the conversations could not be planned")
	for _, tc := range []struct{ stop, wantCode string }{
		{"reopened", "CLAUDE_REOPENED"}, {"restore", "RECOVERY_NEEDED"}, {"start", "RESTORED_OPEN_CLAUDE"},
	} {
		t.Run(tc.stop, func(t *testing.T) {
			checks, restores, starts := 0, 0, 0
			private := errors.New("fixture-private-recovery-detail")
			op := transactionOps{
				Prepare: func() error { return nil }, Quit: func() error { return nil },
				Closed: func() bool { checks++; return tc.stop != "reopened" || checks < 4 },
				Backup: func() (string, error) { return "fixture-backup", nil },
				Active: func() (string, error) { return "fixture-account", nil },
				Seal:   func(string) error { return nil },
				Restore: func(string) error {
					restores++
					if tc.stop == "restore" {
						return private
					}
					return nil
				},
				Start: func() error { starts++; return private },
			}
			_, err := runDesktopTransaction(op, func(string) error { return cause })
			if err == nil || errorCode(err) != tc.wantCode || !errors.Is(err, cause) || !strings.Contains(err.Error(), "the conversations could not be planned") {
				t.Fatalf("missing outcome code or cause: %s %v", errorCode(err), err)
			}
			if strings.Contains(err.Error(), private.Error()) {
				t.Fatalf("unsafe recovery diagnostic: %v", err)
			}
			wantRestores, wantStarts := 1, 1
			switch tc.stop {
			case "reopened":
				wantRestores, wantStarts = 0, 0
			case "restore":
				wantStarts = 0
			}
			if restores != wantRestores || starts != wantStarts {
				t.Fatal("unsafe recovery execution after failure")
			}
		})
	}
}

func TestTransactionRollbackKeepsTheCause(t *testing.T) {
	var restored, started int
	ops := transactionOps{
		Prepare: func() error { return nil },
		Quit:    func() error { return nil },
		Closed:  func() bool { return true },
		Backup:  func() (string, error) { return "tx-1", nil },
		Active:  func() (string, error) { return "acct-a", nil },
		Seal:    func(string) error { return nil },
		Restore: func(string) error { restored++; return nil },
		Start:   func() error { started++; return nil },
	}
	cause := coded("SAVED_SIGNIN_MISMATCH", "the saved sign-in belongs to a different account")
	bk, err := runDesktopTransaction(ops, func(string) error { return cause })
	if bk != "tx-1" || err == nil {
		t.Fatalf("rollback must report the backup and an error, got %q %v", bk, err)
	}
	if !errors.Is(err, cause) || errorCode(err) != "SAVED_SIGNIN_MISMATCH" {
		t.Fatalf("the cause and its code must survive the rollback: %s %v", errorCode(err), err)
	}
	if restored != 1 || started != 1 {
		t.Fatalf("rollback restores once and reopens once, got %d %d", restored, started)
	}

	// Claude still running never reaches the change.
	running := ops
	running.Closed = func() bool { return false }
	changed := false
	_, err = runDesktopTransaction(running, func(string) error { changed = true; return nil })
	if changed || errorCode(err) != "CLAUDE_STILL_OPEN" {
		t.Fatalf("a running Claude refuses before any change: %v %s", changed, errorCode(err))
	}

	// A failed prepare or backup is named and nothing was changed.
	failing := ops
	failing.Backup = func() (string, error) { return "", errors.New("fixture backup failure") }
	_, err = runDesktopTransaction(failing, func(string) error { changed = true; return nil })
	if changed || errorCode(err) != "BACKUP_FAILED" || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a failed backup refuses before any change: %v %s %v", changed, errorCode(err), err)
	}
}
