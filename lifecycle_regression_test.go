package main

import (
	"errors"
	"testing"
)

func TestDesktopReopenedDuringBackupNeverMutates(t *testing.T) {
	checks := 0
	op := transactionOps{
		Prepare: func() error { return nil }, Quit: func() error { return nil },
		Closed: func() bool { checks++; return checks == 1 },
		Backup: func() (string, error) { return "bk", nil }, Active: func() (string, error) { return "A", nil },
		Start:   func() error { t.Fatal("should not start another instance"); return nil },
		Restore: func(string) error { t.Fatal("should not restore under live app"); return nil },
	}
	if _, err := runDesktopTransaction(op, func(string) error { t.Fatal("mutated after app reopened"); return nil }); err == nil {
		t.Fatal("expected pause")
	}
}

func TestDesktopReopenedAfterMutationNeverRestores(t *testing.T) {
	checks := 0
	op := transactionOps{
		Prepare: func() error { return nil }, Quit: func() error { return nil },
		Closed: func() bool { checks++; return checks < 3 },
		Backup: func() (string, error) { return "bk", nil }, Active: func() (string, error) { return "A", nil },
		Seal:    func(string) error { t.Fatal("should not seal live app writes"); return nil },
		Restore: func(string) error { t.Fatal("should not restore under live app"); return nil },
	}
	if _, err := runDesktopTransaction(op, func(string) error { return errors.New("partial change") }); err == nil {
		t.Fatal("expected pause")
	}
}

func TestCorruptStateDoesNotGetReplacedByEmptyState(t *testing.T) {
	dir := t.TempDir()
	write(t, dir+"/state.json", `{"accounts":`)
	s := newSwitcher(dir)
	if !s.stateUnreadable {
		t.Fatal("a settings file that cannot be read must stop the helper from changing anything")
	}
	if err := s.run("Switch account", func() error { t.Fatal("something ran although the settings are unreadable"); return nil }); errorCode(err) != "STATE_UNREADABLE" {
		t.Fatalf("every action is refused with the reason: %v", err)
	}
	if s.snapshot().Blocked {
		t.Fatal("an unreadable settings file is not an interrupted switch; the menu must not offer the fix for one")
	}
	s.saveState()
	if got := read(t, dir+"/state.json"); got != `{"accounts":` {
		t.Fatal("corrupt state overwritten")
	}
}
