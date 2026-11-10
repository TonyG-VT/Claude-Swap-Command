package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusReportSaysWhatHappenedInPlainWords(t *testing.T) {
	f := newSwitchFixture(t)
	// One conversation that cannot follow: a message is still queued for it.
	f.record(f.a, 3, map[string]any{"title": "Fix the login page"})
	write(t, filepath.Join(f.d.sessionsDir(), f.a.Account, f.a.Org, "waiting-input", "local_"+carryID(3)+".json"),
		`{"v":1,"sessionId":"local_`+carryID(3)+`","entries":[{"id":"queued"}],"cleanShutdown":true,"savedAt":10}`)
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatalf("switch: %v", err)
	}
	report := f.s.statusReport(f.now)
	for _, want := range []string{
		"Claude Swap Command " + version,
		"Claude 1.0: self-test passed",
		"Account 1: Sam (a@example.test, Max)",
		"● Account 2: Sam (b@example.test, Max)",
		"Account 3: Sam B (c@example.test, Max)",
		"Last switch: Account 1 → Account 2",
		"Claude accepted the sign-in",
		"1 new, 1 updated",
		`"Fix the login page": it has a message waiting to be sent`,
		"Data folder: " + f.s.dir,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report must contain %q", want)
		}
	}
	for _, secret := range []string{"token-", "cookies-", f.a.Account, f.b.Org} {
		if strings.Contains(report, secret) {
			t.Errorf("the report must not contain %q", secret)
		}
	}
	if t.Failed() {
		t.Log(report)
	}

	// After the helper restarts, the last switch is still described, from the
	// journal (which never holds titles).
	again := newSwitcher(f.s.dir)
	again.env = f.s.env
	again.st.Accounts = f.s.st.Accounts
	restarted := again.statusReport(f.now)
	if !strings.Contains(restarted, "Last switch: Account 1 → Account 2") || !strings.Contains(restarted, "1 new, 1 updated") ||
		!strings.Contains(restarted, "1 left as it was (it has a message waiting to be sent)") {
		t.Fatalf("after a restart the last switch comes from the journal:\n%s", restarted)
	}
	if strings.Contains(restarted, "Fix the login page") {
		t.Fatal("titles are never kept in the journal")
	}
}

func TestStatusReportExplainsAPausedSelfTestAndAFailedSwitch(t *testing.T) {
	f := newSwitchFixture(t)
	f.closeErr = coded("CLAUDE_STILL_OPEN", "Claude did not close, so nothing was changed.")
	f.s.run("Switch account", func() error { return f.s.switchTo(f.b.Account) })
	report := f.s.statusReport(f.now)
	if !strings.Contains(report, "Switch account failed [CLAUDE_STILL_OPEN]: Claude did not close, so nothing was changed.") {
		t.Fatalf("the failed action is stated with its code:\n%s", report)
	}
	if !strings.Contains(report, "● Account 1") {
		t.Fatalf("the first account is still the active one:\n%s", report)
	}

	f = newSwitchFixture(t)
	f.vendorErr = context.Canceled
	f.s.noteSelfCheck(f.s.env.selfCheck(context.Background()))
	report = f.s.statusReport(f.now)
	if !strings.Contains(report, "switching is paused") || !strings.Contains(report, "✗ Claude app") {
		t.Fatalf("a failing self-test is shown probe by probe:\n%s", report)
	}
}

func TestEverySkipCodeHasAPlainReason(t *testing.T) {
	for _, code := range []string{"RECORD_UNREADABLE", "RECORD_MALFORMED", "QUEUED_MESSAGE", "REMOTE_RECORD", "STILL_IMPORTING",
		"DELETED_ON_DESTINATION", "TRANSCRIPT_OWNED_ELSEWHERE", "DESTINATION_BRANCHED", "NAMED_IN_UNKNOWN_FILE", "DESTINATION_UNREADABLE"} {
		if reason := skipReason(code); reason == "" || strings.Contains(reason, code) || strings.Contains(reason, "_") {
			t.Errorf("%s has no plain reason: %q", code, reason)
		}
	}
	if got := skipReason("SOMETHING_NEW"); got != "it could not be carried safely (SOMETHING_NEW)" {
		t.Errorf("an unknown code is still explained: %q", got)
	}
}

func TestSwitchMessageSaysWhatCameAlong(t *testing.T) {
	f := newSwitchFixture(t)
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatal(err)
	}
	if title, body := f.s.switchMessage(); title != "Now on Account 2" || body != "2 conversations came with you (1 new, 1 updated)." {
		t.Fatalf("%q / %q", title, body)
	}
	if err := f.s.switchTo(f.a.Account); err != nil {
		t.Fatal(err)
	}
	if title, body := f.s.switchMessage(); title != "Now on Account 1" || body != "Your conversations were already up to date." {
		t.Fatalf("%q / %q", title, body)
	}
	for want, outcome := range map[string]*switchOutcome{
		"1 conversation came with you (1 brought in line). 2 were left as they were; Status… says why.": {From: f.a.Account, To: f.b.Account, Verdict: verdictConfirmed,
			Carry: &carryPlan{writes: []carryWrite{{Action: carryRefreshed}}, Skipped: []carrySkip{{Code: "QUEUED_MESSAGE"}, {Code: "REMOTE_RECORD"}}}},
		"The account switched, but your conversations were not carried over: the folder is too large. Status… has the details.": {From: f.a.Account, To: f.b.Account, Verdict: verdictConfirmed,
			CarryNote: "the folder is too large"},
		"Claude was signed out before, so there were no conversations to carry over.": {To: f.b.Account, Verdict: verdictConfirmed},
		"Your conversations were already up to date. Claude has not confirmed the account yet; check that it shows the right one.": {From: f.a.Account, To: f.b.Account, Verdict: verdictUnconfirmed,
			Carry: &carryPlan{Identical: 3}},
	} {
		f.s.lastOutcome = outcome
		if _, body := f.s.switchMessage(); body != want {
			t.Errorf("body = %q, want %q", body, want)
		}
	}
}

func TestDryRunSaysWhatEachSwitchWouldCarryWithoutChangingAnything(t *testing.T) {
	f := newSwitchFixture(t)
	before := read(t, f.recordPath(f.b, 2))
	report, ok := f.s.dryRunReport(f.now)
	if !ok {
		t.Fatalf("the dry run must succeed:\n%s", report)
	}
	for _, want := range []string{
		"Claude is signed in to Account 1",
		"Account 1 → Account 2: 1 new, 1 updated",
		"Account 1 → Account 3: 2 new",
		"saved sign-in for Account 2: complete and its own",
		"saved sign-in for Account 3: complete and its own",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the dry run must contain %q", want)
		}
	}
	if t.Failed() {
		t.Log(report)
	}
	if read(t, f.recordPath(f.b, 2)) != before || f.closes != 0 || f.starts != 0 || len(f.journal()) != 0 {
		t.Fatal("a dry run changes nothing and leaves Claude alone")
	}
	// A saved sign-in that would be refused is said so here, not at switch time.
	if err := os.RemoveAll(f.s.profileDir(f.c.Account)); err != nil {
		t.Fatal(err)
	}
	report, ok = f.s.dryRunReport(f.now)
	if ok || !strings.Contains(report, "saved sign-in for Account 3: NOT USABLE [NO_SAVED_SIGNIN]") {
		t.Fatalf("a missing saved sign-in fails the dry run:\n%s", report)
	}
}

func TestStateFromTheEarlierHelperIsKeptOnceAndStillWorks(t *testing.T) {
	dir := t.TempDir()
	earlier := `{
  "schemaVersion": 4,
  "automation": {"enabled": false},
  "guided": {"step": "connections", "paused": true},
  "projectTransfers": [{"sourceAccount": "` + carryID(1) + `", "sourceOrg": "` + carryID(2) + `", "targetAccount": "` + carryID(3) + `", "targetOrg": "` + carryID(4) + `", "project": "/work/project"}],
  "accounts": [
    {"alias": "Work", "uuid": "` + carryID(1) + `", "org": "` + carryID(2) + `", "name": "Sam", "email": "one@example.test", "plan": "Max",
     "usage": {"session": 17, "sessionKnown": true, "weekly": 83, "weeklyKnown": true, "checkedAt": "2026-11-09T10:00:00Z", "sessionReset": "2026-11-09T12:00:00Z", "weeklyReset": "2026-11-12T12:00:00Z", "extraUsageKnown": false, "extraUsageEnabled": false},
     "problem": "Usage refresh rate limited; waiting to refresh",
     "refreshHealth": {"version": 1, "code": "rate_limited", "failures": 1, "observedAt": "2026-11-09T10:00:00Z", "retryAt": "2026-11-09T10:02:00Z"},
     "subscription": {}},
    {"uuid": "` + carryID(3) + `", "org": "` + carryID(4) + `", "name": "Sam B", "email": "two@example.test", "plan": "Max", "problem": "signed out, add it again", "subscription": {}}
  ],
  "pilotPassed": false, "autoSwitch": false, "threshold": 96, "spendWeekly": false,
  "lastSwitch": "2026-11-09T13:51:11-05:00", "handSwitch": "2026-11-09T13:51:11-05:00", "lastFrom": "` + carryID(3) + `", "noUpdateCheck": true
}`
	write(t, filepath.Join(dir, "state.json"), earlier)
	if err := keepStateFromBefore(dir); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(dir, "state.json.before-"+version)
	if read(t, kept) != earlier {
		t.Fatal("the earlier helper's settings are kept exactly")
	}
	s := newSwitcher(dir)
	if s.stateUnreadable || len(s.st.Accounts) != 2 || s.st.Accounts[0].Alias != "Work" || s.st.Accounts[0].Usage == nil || s.st.Accounts[0].Usage.Weekly != 83 {
		t.Fatalf("accounts, names and numbers carry over: %+v", s.st)
	}
	if got := s.st.Accounts[0].Problem; got != problemUsageRateLimited {
		t.Fatalf("an old problem label is put in today's words: %q", got)
	}
	if got := s.st.Accounts[1].Problem; got != "" {
		t.Fatalf("a label with nothing behind it is dropped: %q", got)
	}
	if !s.st.UsageChecksPaused || !s.snapshot().Paused {
		t.Fatal("\"Pause usage checks\" carries over from the earlier helper's file")
	}
	s.mu.Lock()
	s.saveState()
	s.mu.Unlock()
	saved := read(t, filepath.Join(dir, "state.json"))
	if !strings.Contains(saved, `"schemaVersion": 5`) || !strings.Contains(saved, `"usageChecksPaused": true`) {
		t.Fatalf("the settings are saved in this version's form:\n%s", saved)
	}
	// What only the earlier design used is not written again. It is not
	// lost: the exact earlier file was kept above.
	for _, gone := range []string{"projectTransfers", "guided", "automation", "pilotPassed"} {
		if strings.Contains(saved, `"`+gone+`"`) {
			t.Fatalf("a setting nothing uses any more was written again: %s", gone)
		}
	}
	// A later start reads the new form back.
	if again := newSwitcher(dir); again.stateUnreadable || !again.st.UsageChecksPaused || len(again.st.Accounts) != 2 {
		t.Fatal("the saved settings are read back")
	}
	// Only the first time: later runs must not overwrite the kept copy.
	if err := keepStateFromBefore(dir); err != nil || read(t, kept) != earlier {
		t.Fatalf("the kept copy is written once: %v", err)
	}
	// Settings from a newer helper are never touched.
	newer := t.TempDir()
	write(t, filepath.Join(newer, "state.json"), `{"schemaVersion": 6, "accounts": []}`)
	if s := newSwitcher(newer); !s.stateUnreadable {
		t.Fatal("settings written by a newer helper are left alone")
	}
}
