package main

import (
	"strings"
	"testing"
	"time"
)

var menuNow = time.Date(2026, time.November, 10, 10, 0, 0, 0, time.Local)

func menuSnapshot() snapshot {
	usage := func(session, weekly float64, reset time.Time) *Usage {
		return &Usage{Session: session, SessionKnown: true, SessionReset: menuNow.Add(2 * time.Hour),
			Weekly: weekly, WeeklyKnown: true, WeeklyReset: reset, CheckedAt: menuNow.Add(-time.Minute)}
	}
	sat := time.Date(2026, time.November, 14, 4, 0, 0, 0, time.Local)
	return snapshot{
		Threshold: defaultThreshold,
		Active:    carryID(2),
		Accounts: []Account{
			{UUID: carryID(1), Name: "Sam", Email: "one@example.test", Plan: "Max", Usage: usage(0, 13, sat)},
			{UUID: carryID(2), Alias: "Work", Name: "Sam B", Email: "two@example.test", Plan: "Max", Usage: usage(17, 83, sat)},
			{UUID: carryID(3), Email: "three@example.test", Plan: "Pro"},
		},
	}
}

func TestMenuNamesEveryAccountByNumberAndShowsItsUsage(t *testing.T) {
	m := buildMenu(menuSnapshot(), menuNow)
	if m.Header != "Account 2 is active" {
		t.Fatalf("header = %q", m.Header)
	}
	if len(m.Accounts) != 3 {
		t.Fatalf("rows = %d", len(m.Accounts))
	}
	first, second, third := m.Accounts[0], m.Accounts[1], m.Accounts[2]
	if first.Title != "Switch to Account 1: Sam (one@example.test, Max)" || first.Active || !first.Enabled {
		t.Fatalf("first row: %+v", first)
	}
	if first.Detail != "5h 0% · week 13%, resets Sat 04:00" {
		t.Fatalf("first detail = %q", first.Detail)
	}
	// The active account is ticked, is not offered as a switch, and uses the
	// name the person gave it.
	if second.Title != "Account 2: Work (two@example.test, Max)" || !second.Active || second.Enabled {
		t.Fatalf("second row: %+v", second)
	}
	// No name at all: the email is the name, and is not repeated.
	if third.Title != "Switch to Account 3: three@example.test (Pro)" || third.Detail != "usage not checked yet" {
		t.Fatalf("third row: %+v", third)
	}
	if first.ID != carryID(1) || second.ID != carryID(2) || third.ID != carryID(3) {
		t.Fatal("each row carries the account it stands for")
	}
	if !m.AddEnabled || m.AddTitle != "Add account…" || m.ShowFix || len(m.Imports) != 0 {
		t.Fatalf("the rest of the menu: %+v", m)
	}
	if !strings.HasPrefix(m.Tooltip, "Account 2: Work") || !strings.Contains(m.Tooltip, "5h 17%") || m.IconPercent != 17 {
		t.Fatalf("tooltip/icon: %q %v", m.Tooltip, m.IconPercent)
	}
}

func TestMenuWhileSomethingIsRunningOffersNothingAndSaysWhat(t *testing.T) {
	snap := menuSnapshot()
	snap.Busy, snap.Status = true, "Waiting for Claude to close…"
	m := buildMenu(snap, menuNow)
	if m.Header != "Waiting for Claude to close…" {
		t.Fatalf("header = %q", m.Header)
	}
	for _, row := range m.Accounts {
		if row.Enabled {
			t.Fatalf("no switch is offered while one is running: %+v", row)
		}
	}
	if m.AddEnabled {
		t.Fatal("Add account is not offered while something is running")
	}
}

func TestMenuHeaderSaysWhatNeedsAttention(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*snapshot)
		want   string
	}{
		"nobody signed in": {func(s *snapshot) { s.Active = "" }, "Claude is signed out"},
		"an account the helper has not saved": {func(s *snapshot) { s.Active = carryID(9) },
			"Claude is signed in to an account that is not saved yet"},
		"waiting for a new sign-in": {func(s *snapshot) { s.Active, s.Adding = "", true },
			"Sign in to the new account in Claude"},
		"an interrupted switch": {func(s *snapshot) { s.Blocked = true },
			"An earlier switch was interrupted"},
		"the last action failed": {func(s *snapshot) { s.Status = "Switch account failed [CLAUDE_STILL_OPEN]: Claude did not close." },
			"Switch account failed [CLAUDE_STILL_OPEN]: Claude did not close."},
		"no accounts yet": {func(s *snapshot) { s.Accounts, s.Active = nil, "" }, "Open Claude and sign in"},
	} {
		t.Run(name, func(t *testing.T) {
			snap := menuSnapshot()
			tc.change(&snap)
			m := buildMenu(snap, menuNow)
			if !strings.Contains(m.Header, tc.want) {
				t.Fatalf("header = %q, want it to contain %q", m.Header, tc.want)
			}
		})
	}
	snap := menuSnapshot()
	snap.Adding, snap.Active = true, ""
	if m := buildMenu(snap, menuNow); m.AddTitle != "Add account… (waiting for you to sign in to Claude)" {
		t.Fatalf("add title = %q", m.AddTitle)
	}
	snap = menuSnapshot()
	snap.Blocked = true
	if m := buildMenu(snap, menuNow); !m.ShowFix {
		t.Fatal("Fix an interrupted switch… is offered when one exists")
	}
}

func TestMenuKeepsLongHeadersShort(t *testing.T) {
	snap := menuSnapshot()
	snap.Status = strings.Repeat("a very long explanation ", 12)
	if m := buildMenu(snap, menuNow); len([]rune(m.Header)) > menuHeaderLimit || !strings.HasSuffix(m.Header, "…") {
		t.Fatalf("header has %d characters: %q", len([]rune(m.Header)), m.Header)
	}
}

func TestMenuRetiredAccountsAreShownButNotOffered(t *testing.T) {
	snap := menuSnapshot()
	snap.Accounts[0].Retired = true
	row := buildMenu(snap, menuNow).Accounts[0]
	if row.Enabled || !strings.HasSuffix(row.Title, "· retired") || strings.HasPrefix(row.Title, "Switch to") {
		t.Fatalf("retired row: %+v", row)
	}
}

func TestMenuShowsTheLastSwitchForAWhile(t *testing.T) {
	snap := menuSnapshot()
	snap.LastOutcome = &switchOutcome{From: carryID(1), To: carryID(2), Verdict: verdictConfirmed, At: menuNow.Add(-3 * time.Minute),
		Carry: &carryPlan{writes: []carryWrite{{Action: carryNew}, {Action: carryNew}, {Action: carryUpdated}}, Identical: 5}}
	if m := buildMenu(snap, menuNow); m.Header != "Account 2 is active · 3 conversations carried over" {
		t.Fatalf("header = %q", m.Header)
	}
	snap.LastOutcome.Carry, snap.LastOutcome.CarryNote = nil, "the conversation folder could not be read"
	if m := buildMenu(snap, menuNow); m.Header != "Account 2 is active · conversations were not carried over (see Status…)" {
		t.Fatalf("header = %q", m.Header)
	}
	snap.LastOutcome.At = menuNow.Add(-2 * time.Hour)
	if m := buildMenu(snap, menuNow); m.Header != "Account 2 is active" {
		t.Fatalf("an old switch is no longer mentioned: %q", m.Header)
	}
}

// The numbers stay visible when a check fails. Eight switches were once
// refused, and the usage hidden, only because the service was rate-limiting.
func TestUsageDetailKeepsTheNumbersAndSaysHowOldTheyAre(t *testing.T) {
	a := menuSnapshot().Accounts[1]
	if got := usageDetail(a, defaultThreshold, menuNow); got != "5h 17% · week 83%, resets Sat 04:00" {
		t.Fatalf("fresh = %q", got)
	}
	a.Problem = "usage check is being rate-limited"
	if got := usageDetail(a, defaultThreshold, menuNow); got != "5h 17% · week 83%, resets Sat 04:00 · usage check is being rate-limited" {
		t.Fatalf("with a problem = %q", got)
	}
	a.Problem = ""
	a.Usage.CheckedAt = menuNow.Add(-3 * time.Hour)
	if got := usageDetail(a, defaultThreshold, menuNow); got != "5h 17% · week 83%, resets Sat 04:00 · checked 3 hours ago" {
		t.Fatalf("old = %q", got)
	}
	a.Usage.CheckedAt = menuNow.Add(-50 * time.Hour)
	if got := usageDetail(a, defaultThreshold, menuNow); !strings.HasSuffix(got, "· checked 2 days ago") {
		t.Fatalf("days old = %q", got)
	}
	a.Usage, a.Problem = nil, "usage check is being rate-limited"
	if got := usageDetail(a, defaultThreshold, menuNow); got != "usage not checked yet · usage check is being rate-limited" {
		t.Fatalf("no numbers = %q", got)
	}
}

func TestResetTextNamesTheDateForTimesFarAway(t *testing.T) {
	for want, at := range map[string]time.Time{
		"14:30":            menuNow.Add(4*time.Hour + 30*time.Minute),
		"Sat 04:00":        time.Date(2026, time.November, 14, 4, 0, 0, 0, time.Local),
		"Sun 09:00":        time.Date(2026, time.November, 8, 9, 0, 0, 0, time.Local), // two days ago
		"Tue 20 Oct 09:00": time.Date(2026, time.October, 20, 9, 0, 0, 0, time.Local),
		"Fri 20 Nov 10:00": time.Date(2026, time.November, 20, 10, 0, 0, 0, time.Local),
	} {
		if got := resetText(at, menuNow); got != want {
			t.Errorf("resetText(%v) = %q, want %q", at, got, want)
		}
	}
}

func TestAccountTitleFallsBackSensibly(t *testing.T) {
	for want, a := range map[string]Account{
		"Account 4: Sam (t@example.test, Max)": {Name: "Sam", Email: "t@example.test", Plan: "Max"},
		"Account 4: Sam (t@example.test)":      {Name: "Sam", Email: "t@example.test"},
		"Account 4: t@example.test (Max)":      {Email: "t@example.test", Plan: "Max"},
		"Account 4: Sam (Max)":                 {Name: "Sam", Plan: "Max"},
		"Account 4":                            {},
	} {
		if got := accountTitle(4, a); got != want {
			t.Errorf("accountTitle = %q, want %q", got, want)
		}
	}
}

// An account that is not in use has no 5-hour window running: the service
// sends "five_hour": null. That is 0% used, not "unknown".
func TestUsageOfAnIdleAccountShowsZeroNotUnknown(t *testing.T) {
	reset := menuNow.Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	idle, err := parseUsage([]byte(`{"five_hour": null, "seven_day": {"utilization": 13, "resets_at": "`+reset+`"}}`), menuNow)
	if err != nil {
		t.Fatal(err)
	}
	if idle.SessionKnown || !idle.SessionIdle {
		t.Fatalf("a null window is idle, and still not a measured number: %+v", idle)
	}
	if got := usageDetail(Account{Usage: idle}, defaultThreshold, menuNow); got != "5h 0% · week 13%, resets Sat 10:00" {
		t.Fatalf("detail = %q", got)
	}
	missing, err := parseUsage([]byte(`{"seven_day": {"utilization": 13, "resets_at": "`+reset+`"}}`), menuNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := usageDetail(Account{Usage: missing}, defaultThreshold, menuNow); got != "5h unknown · week 13%, resets Sat 10:00" {
		t.Fatalf("a window the service did not mention stays unknown: %q", got)
	}
}

// The service has a second way of saying that no 5-hour window is running:
// a window with nothing used and no reset time yet. An account that has not
// been used at all this week looks like that.
func TestUsageWindowWithNothingUsedAndNoResetTimeIsIdleToo(t *testing.T) {
	reset := menuNow.Add(4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	unused, err := parseUsage([]byte(`{"five_hour": {"utilization": 0, "resets_at": null}, "seven_day": {"utilization": 0, "resets_at": "`+reset+`"}}`), menuNow)
	if err != nil {
		t.Fatal(err)
	}
	if unused.SessionKnown || !unused.SessionIdle {
		t.Fatalf("nothing used and no reset time is an idle window, still not a measured number: %+v", unused)
	}
	if got := usageDetail(Account{Usage: unused}, defaultThreshold, menuNow); got != "5h 0% · week 0%, resets Sat 10:00" {
		t.Fatalf("detail = %q", got)
	}
	// Something used but no reset time does not add up: that stays unknown.
	odd, err := parseUsage([]byte(`{"five_hour": {"utilization": 40, "resets_at": null}, "seven_day": {"utilization": 0, "resets_at": "`+reset+`"}}`), menuNow)
	if err != nil {
		t.Fatal(err)
	}
	if odd.SessionKnown || odd.SessionIdle {
		t.Fatalf("usage without a reset time is unknown, not idle: %+v", odd)
	}
	// The switched-off automatic rules must not start treating such a window
	// as covered: for them it stays "not measured".
	if w := parseUsageWindow([]byte(`{"utilization": 0, "resets_at": null}`)); w.Known || w.Unspecified {
		t.Fatalf("an idle window is neither measured nor unspecified: %+v", w)
	}
}
