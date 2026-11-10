package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestTransactionOrderingAndFailures(t *testing.T) {
	for _, fail := range []string{"", "prepare", "quit", "backup", "change", "seal", "restore", "start"} {
		t.Run(fail, func(t *testing.T) {
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if name == fail || (fail == "restore" && name == "change") {
					return errors.New("fixture failure")
				}
				return nil
			}
			op := transactionOps{
				Prepare: func() error { return step("prepare") }, Quit: func() error { return step("quit") }, Closed: func() bool { calls = append(calls, "closed"); return true },
				Backup: func() (string, error) { return "backup", step("backup") }, Active: func() (string, error) { return "A", step("active") },
				Seal: func(string) error { return step("seal") }, Restore: func(string) error { return step("restore") }, Start: func() error { return step("start") },
			}
			_, err := runDesktopTransaction(op, func(string) error { return step("change") })
			if (err != nil) != (fail != "") {
				t.Fatalf("failure=%s err=%v", fail, err)
			}
			if fail == "" && !reflect.DeepEqual(calls, []string{"prepare", "quit", "closed", "backup", "active", "closed", "change", "closed", "seal", "start"}) {
				t.Fatalf("order: %v", calls)
			}
			if fail == "prepare" && len(calls) != 1 {
				t.Fatalf("unsafe work after prepare: %v", calls)
			}
			if fail == "restore" && calls[len(calls)-1] != "restore" {
				t.Fatalf("restarted after failed rollback: %v", calls)
			}
		})
	}
}

func TestTransactionNeverWritesWhileDesktopStillRunning(t *testing.T) {
	op := transactionOps{Prepare: func() error { return nil }, Quit: func() error { return nil }, Closed: func() bool { return false }, Backup: func() (string, error) { t.Fatal("backup called while live"); return "", nil }}
	if _, err := runDesktopTransaction(op, func(string) error { t.Fatal("changed while live"); return nil }); err == nil {
		t.Fatal("expected refusal")
	}
}

func TestUncertainLaunchDoesNotRollbackLiveDesktop(t *testing.T) {
	checks := 0
	op := transactionOps{Prepare: func() error { return nil }, Quit: func() error { return nil }, Closed: func() bool { checks++; return checks <= 3 }, Backup: func() (string, error) { return "bk", nil }, Active: func() (string, error) { return "A", nil }, Seal: func(string) error { return nil }, Restore: func(string) error { t.Fatal("rollback underneath live app"); return nil }, Start: func() error { return errors.New("uncertain") }}
	if _, err := runDesktopTransaction(op, func(string) error { return nil }); err == nil {
		t.Fatal("expected pause")
	}
}
