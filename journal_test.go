package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalWritesOnePrivateLinePerEvent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j := newSwitchJournal(dir)
	j.manualSwitch("acct-a", "acct-b", &journalCarry{Outcome: "full", New: 3, Refreshed: 2, Identical: 15, KeptNewer: 1}, "confirmed", nil)
	j.manualSwitch("acct-a", "acct-b", nil, "", errors.New("fixture-private-value at /private/fixture/account.json"))
	j.addAccount("reopen_for_sign_in", "acct-a", "", coded("CLAUDE_STILL_OPEN", "Claude is still open, so nothing was changed."))

	path := filepath.Join(dir, "switches.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fixture-private") || strings.Contains(string(data), "/private/") {
		t.Fatalf("raw error text reached the journal: %s", data)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("want one line per event, got %d: %s", len(lines), data)
	}
	var entries []journalEntry
	for _, line := range lines {
		var e journalEntry
		if err := json.Unmarshal(line, &e); err != nil || e.At.IsZero() {
			t.Fatalf("each line is one complete entry with a time: %v %s", err, line)
		}
		entries = append(entries, e)
	}
	ok, failed, add := entries[0], entries[1], entries[2]
	if ok.Kind != "manual_switch" || ok.Result != "ok" || ok.From != "acct-a" || ok.To != "acct-b" || ok.Verdict != "confirmed" || ok.Carry == nil || ok.Carry.New != 3 || ok.Carry.Refreshed != 2 {
		t.Fatalf("successful switch entry: %+v", ok)
	}
	if failed.Result != "failed" || failed.Code != "UNCLASSIFIED" || failed.Detail != "an unexpected problem" {
		t.Fatalf("failed switch entry must carry a code and a described cause: %+v", failed)
	}
	if add.Kind != "add_account" || add.Stage != "reopen_for_sign_in" || add.Code != "CLAUDE_STILL_OPEN" || add.Detail != "Claude is still open, so nothing was changed." {
		t.Fatalf("add-account entry: %+v", add)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal file must be private: %v %v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("journal folder must be private: %v %v", info.Mode().Perm(), err)
	}
	var nilJournal *switchJournal
	nilJournal.note("note", "a missing journal is not a crash")
}

func TestJournalKeepsOnePreviousFileOnceItPassesOneMegabyte(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "switches.jsonl")
	old := bytes.Repeat([]byte(`{"kind":"note"}`+"\n"), (1<<20)/16+10)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	newSwitchJournal(dir).note("note", "first line of the new file")
	rotated, err := os.ReadFile(filepath.Join(dir, "switches.1.jsonl"))
	if err != nil || len(rotated) != len(old) {
		t.Fatalf("the full file is kept as switches.1.jsonl: %d %v", len(rotated), err)
	}
	current, err := os.ReadFile(path)
	if err != nil || bytes.Count(current, []byte("\n")) != 1 || !bytes.Contains(current, []byte("first line of the new file")) {
		t.Fatalf("the new file starts with the new entry: %q %v", current, err)
	}
}
