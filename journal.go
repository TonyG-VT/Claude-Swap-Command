package main

// The switch journal is the record of what the helper did: one JSON line per
// event under <data>/journal/switches.jsonl. Entries hold account and record
// identifiers, counts, codes and described causes; never tokens, cookies,
// email addresses, titles, prompts or transcript text.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const journalRotateBytes = 1 << 20

type journalSkip struct {
	Record string `json:"record"` // first 8 characters of the record id
	Code   string `json:"code"`
}

// journalCarry summarises what happened to the conversations of one switch.
type journalCarry struct {
	Outcome   string        `json:"outcome"` // full, partial or none
	New       int           `json:"new,omitempty"`
	Updated   int           `json:"updated,omitempty"`       // the source had newer messages
	Refreshed int           `json:"refreshed,omitempty"`     // same messages, other details differed; the source won
	Identical int           `json:"identical,omitempty"`     // already the same on both accounts
	KeptNewer int           `json:"keptNewer,omitempty"`     // the destination had newer messages
	KeptCache int           `json:"keptCacheOnly,omitempty"` // only cache details differed; left alone
	Skipped   []journalSkip `json:"skipped,omitempty"`
	Ignored   []string      `json:"ignoredEntries,omitempty"`  // unknown file or folder names, identifiers masked
	NewFields []string      `json:"newRecordFields,omitempty"` // record fields this helper has not seen before
	Reason    string        `json:"reason,omitempty"`          // why nothing was carried
}

type journalEntry struct {
	At      time.Time     `json:"at"`
	Kind    string        `json:"kind"` // manual_switch, add_account, import_old_signin, self_check, recover, note
	Stage   string        `json:"stage,omitempty"`
	From    string        `json:"from,omitempty"`
	To      string        `json:"to,omitempty"`
	Result  string        `json:"result,omitempty"`  // ok or failed
	Verdict string        `json:"verdict,omitempty"` // what was seen after Claude reopened
	Code    string        `json:"code,omitempty"`
	Detail  string        `json:"detail,omitempty"`
	Claude  string        `json:"claudeVersion,omitempty"`
	Carry   *journalCarry `json:"carry,omitempty"`
}

type switchJournal struct {
	mu  sync.Mutex
	dir string
}

func newSwitchJournal(dir string) *switchJournal { return &switchJournal{dir: dir} }

func (j *switchJournal) append(e journalEntry) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.Detail = redactSecrets(e.Detail)
	if err := ensurePrivateDirectory(j.dir); err != nil {
		log.Printf("journal unavailable: %s", describeCause(err))
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	path := filepath.Join(j.dir, "switches.jsonl")
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() > journalRotateBytes {
		// One previous file is kept; an older one is replaced.
		_ = os.Rename(path, filepath.Join(j.dir, "switches.1.jsonl"))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("journal unavailable: %s", describeCause(err))
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
	_ = f.Sync()
}

func (e journalEntry) withOutcome(err error) journalEntry {
	e.Result = "ok"
	if err != nil {
		e.Result, e.Code, e.Detail = "failed", errorCode(err), sanitizeError(err)
	}
	return e
}

// manualSwitch records one switch made from the menu: what was carried, what
// was seen after Claude reopened, and the code and cause when it failed.
func (j *switchJournal) manualSwitch(from, to string, carry *journalCarry, verdict string, err error) {
	j.append(journalEntry{Kind: "manual_switch", From: from, To: to, Carry: carry, Verdict: verdict}.withOutcome(err))
}

// addAccount records each stage of enrolling another account: reopening
// Claude on its sign-in screen (from = the account being saved), then
// verifying the new sign-in (to = the account that signed in).
func (j *switchJournal) addAccount(stage, from, to string, err error) {
	j.append(journalEntry{Kind: "add_account", Stage: stage, From: from, To: to}.withOutcome(err))
}

func (j *switchJournal) note(kind, detail string) {
	j.append(journalEntry{Kind: kind, Detail: detail})
}
