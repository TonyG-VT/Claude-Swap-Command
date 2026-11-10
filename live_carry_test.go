package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A read-only rehearsal against this Mac's real data: for every pair of
// enrolled accounts, plan the carry-over and print counts and codes only.
// Nothing is written. Run with CLAUDE_SWAP_TEST_LIVE_PLAN=1.
func TestLiveCarryDryRun(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_PLAN") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_PLAN=1 to plan against this Mac's real conversation folders (read-only)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude-swap-command", "state.json"))
	if err != nil {
		t.Skip("no helper state on this Mac")
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("state unreadable: %s", describeCause(err))
	}
	d := claudeDesktop()
	for i, from := range st.Accounts {
		for j, to := range st.Accounts {
			if i == j {
				continue
			}
			p, err := planCarry(d, carryNamespace{from.UUID, from.Org}, carryNamespace{to.UUID, to.Org}, nil)
			if err != nil {
				t.Errorf("Account %d -> Account %d: plan failed [%s]: %s", i+1, j+1, errorCode(err), sanitizeError(err))
				continue
			}
			s := p.summary()
			t.Logf("Account %d -> Account %d: %s; new=%d updated=%d refreshed=%d identical=%d keptNewer=%d keptCacheOnly=%d skipped=%v ignored=%v newFields=%v",
				i+1, j+1, s.Outcome, s.New, s.Updated, s.Refreshed, s.Identical, s.KeptNewer, s.KeptCache, s.Skipped, s.Ignored, s.NewFields)
		}
	}
}

// The self-test against the real Claude on this Mac. Read-only: it checks the
// app's signature, decodes who is signed in, and looks for the session cookie
// by name. It prints probe names, codes and this helper's own sentences only.
// Run with CLAUDE_SWAP_TEST_LIVE_PLAN=1.
func TestLiveSelfCheck(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_PLAN") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_PLAN=1 to run the self-test against this Mac's real Claude (read-only)")
	}
	check := runSelfCheck(t.Context(), liveSelfCheckEnv())
	t.Logf("%s", check.summary())
	for _, probe := range check.Probes {
		t.Logf("  %-15s ok=%-5v %s %s", probe.Name, probe.OK, probe.Code, probe.Detail)
	}
	t.Logf("  signed in: %v; one account: %v; organisation known: %v", !check.SignIn.SignedOut(), !check.SignIn.Multiple(), check.SignIn.Org != "")
	if !check.CanSwitch {
		t.Errorf("the self-test would pause switching on this Mac: %v", check.blocker())
	}
}

// The offline check of every saved sign-in on this Mac. Read-only; prints
// account numbers, ages and codes only. Run with CLAUDE_SWAP_TEST_LIVE_PLAN=1.
func TestLiveSavedSignIns(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_PLAN") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_PLAN=1 to check this Mac's saved sign-ins (read-only)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude-swap-command")
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Skip("no helper state on this Mac")
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("state unreadable: %s", describeCause(err))
	}
	live, err := readSignIn(claudeDesktop().configPath(), decryptValue)
	if err != nil {
		t.Fatalf("live sign-in unreadable [%s]", errorCode(err))
	}
	for i, account := range st.Accounts {
		if account.UUID == live.Account {
			t.Logf("Account %d: signed in right now (its saved copy is refreshed at the next switch away)", i+1)
		}
		saved, err := checkSavedSignIn(filepath.Join(dir, "accounts", account.UUID), account.UUID, decryptValue)
		if err != nil {
			t.Logf("Account %d: saved sign-in NOT usable [%s]: %s", i+1, errorCode(err), sanitizeError(err))
			continue
		}
		t.Logf("Account %d: saved sign-in complete and its own; holds a live entry: %v; saved %.1f days ago", i+1, !saved.Empty, time.Since(saved.SavedAt).Hours()/24)
	}
}

// What the closed-check sees on this Mac right now. Read-only: it lists
// program paths and reports only the kinds that would hold a switch.
// Run with CLAUDE_SWAP_TEST_LIVE_PLAN=1.
func TestLiveClosedCheckSeesClaude(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_PLAN") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_PLAN=1 to classify this Mac's running programs (read-only)")
	}
	table, err := processTable(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	blockers := claudeBlockers(table, claudeDesktop().dir)
	counts := map[string]int{}
	for _, p := range table {
		counts[claudeProcessKind(p.Path, claudeDesktop().dir)]++
	}
	t.Logf("%d programs running; holding a switch: %v (Claude %d, Claude Code tasks %d, updater %d)", len(table), blockers,
		counts["Claude"], counts["a Claude Code task"], counts["Claude's updater"])
	t.Logf("sign-in file marks are readable: %v", signInFileMarks(claudeDesktop()) != "")
}

// The polite quit request, proven on a harmless app. It opens TextEdit in the
// background and asks it to quit. Skipped when TextEdit is already open, so a
// document someone is working on is never touched.
// Run with CLAUDE_SWAP_TEST_LIVE_QUIT=1.
func TestLiveQuitRequestClosesAHarmlessApp(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_QUIT") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_QUIT=1 to prove the quit request on TextEdit")
	}
	const textEdit = "/System/Applications/TextEdit.app/Contents/MacOS/TextEdit"
	running := func() bool {
		table, err := processTable(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range table {
			if p.Path == textEdit {
				return true
			}
		}
		return false
	}
	if running() {
		t.Skip("TextEdit is already open; not touching it")
	}
	if err := exec.Command("/usr/bin/open", "-g", "-j", "-a", "TextEdit").Run(); err != nil {
		t.Fatalf("could not open TextEdit: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !running() {
		if time.Now().After(deadline) {
			t.Fatal("TextEdit did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if accepted := requestQuitDirect("com.apple.TextEdit"); accepted != 1 {
		t.Fatalf("the direct quit request (the one that needs no macOS permission) was accepted by %d apps, want 1", accepted)
	}
	deadline = time.Now().Add(15 * time.Second)
	for running() {
		if time.Now().After(deadline) {
			t.Fatal("TextEdit did not quit after the request")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Log("TextEdit quit after one polite request")
}

// TestLiveBackupDryRun makes the same backup a switch would make, of this
// Mac's real Claude folder, into a temporary place that is removed again. It
// proves the backup step works on the real data and shows how long it takes.
// It reads Claude's files and writes nothing to them. It prints counts and a
// duration only. Run with CLAUDE_SWAP_TEST_LIVE_PLAN=1.
func TestLiveBackupDryRun(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_PLAN") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_PLAN=1 to back up this Mac's real Claude folder into a temporary place (read-only)")
	}
	d := claudeDesktop()
	root := filepath.Join(t.TempDir(), "helper", "backups")
	forgetTransactionsUnder(t, filepath.Dir(filepath.Dir(root)))
	started := time.Now()
	bk, err := backup(d, root)
	if err != nil {
		t.Fatalf("the backup a switch starts with fails on this Mac: [%s] %s", errorCode(err), sanitizeError(err))
	}
	took := time.Since(started)
	tx, err := loadBackup(d, bk)
	if err != nil {
		t.Fatalf("the backup cannot be read back: %s", sanitizeError(err))
	}
	existing := 0
	for _, entry := range tx.manifest.Entries {
		if entry.Before.Exists {
			existing++
		}
	}
	t.Logf("backup of %d files (%d entries) took %s", existing, len(tx.manifest.Entries), took.Round(time.Millisecond))
	if took > 30*time.Second {
		t.Errorf("the backup takes too long for a switch: %s", took)
	}
}

// TestLiveUsageShape asks the usage service about each account that is not
// signed in right now and prints only the SHAPE of the two usage windows:
// which fields are there and of what kind, never a value. It exists to find
// out how the service describes a window that is not running.
// Run with CLAUDE_SWAP_TEST_LIVE_USAGE=1.
func TestLiveUsageShape(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_USAGE") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_USAGE=1 to ask the usage service once per idle account (prints field kinds only)")
	}
	dir, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	s := newSwitcher(dir)
	active, _ := s.noticeSignIn()
	kind := func(raw json.RawMessage) string {
		text := string(raw)
		switch {
		case text == "null":
			return "null"
		case len(text) > 0 && text[0] == '"':
			return "text"
		case len(text) > 0 && text[0] == '{':
			return "object"
		case len(text) > 0 && text[0] == '[':
			return "list"
		case text == "true" || text == "false":
			return "yes/no"
		case text == "0" || text == "0.0":
			return "number (zero)"
		}
		return "number (not zero)"
	}
	for i, a := range s.st.Accounts {
		if a.UUID == active || a.Retired {
			continue
		}
		token, err := s.tokenFor(a.UUID, active)
		if err != nil {
			t.Logf("Account %d: no usable saved token (%s)", i+1, describeCause(err))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var body map[string]json.RawMessage
		err = apiGet(ctx, token, "/api/oauth/usage", &body)
		cancel()
		if err != nil {
			t.Logf("Account %d: the service did not answer (%s)", i+1, describeCause(err))
			continue
		}
		for _, name := range []string{"five_hour", "seven_day"} {
			raw, present := body[name]
			if !present {
				t.Logf("Account %d: %s is not in the answer", i+1, name)
				continue
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil || fields == nil {
				t.Logf("Account %d: %s is %s", i+1, name, kind(raw))
				continue
			}
			shape := ""
			for _, key := range []string{"utilization", "resets_at"} {
				if value, ok := fields[key]; ok {
					shape += " " + key + "=" + kind(value)
				} else {
					shape += " " + key + "=missing"
				}
			}
			t.Logf("Account %d: %s is an object:%s (%d fields)", i+1, name, shape, len(fields))
		}
	}
}

// TestLiveFirstRunFindsTheSignedInAccount is what happens on a Mac where the
// helper has never run: an empty data folder (a temporary one here) and a
// Claude that is signed in. One look must find the account, learn its name
// and plan from the service and save it as Account 1. Claude's own files are
// only read; this Mac's real helper data is not touched. It prints yes/no
// answers only. Run with CLAUDE_SWAP_TEST_LIVE_USAGE=1.
func TestLiveFirstRunFindsTheSignedInAccount(t *testing.T) {
	if os.Getenv("CLAUDE_SWAP_TEST_LIVE_USAGE") != "1" {
		t.Skip("set CLAUDE_SWAP_TEST_LIVE_USAGE=1 to rehearse a first run against this Mac's real Claude (asks the usage service once)")
	}
	dir := filepath.Join(t.TempDir(), "fresh-helper")
	if err := ensurePrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	s := newSwitcher(dir)
	if before := s.snapshot(); len(before.Accounts) != 0 || before.Blocked || before.Status != "" {
		t.Fatalf("a fresh helper starts empty and calm: accounts=%d blocked=%v status=%q", len(before.Accounts), before.Blocked, before.Status)
	}
	if header := buildMenu(s.snapshot(), time.Now()).Header; header != "Open Claude and sign in. Your account appears here by itself." {
		t.Fatalf("what the menu says before the first look: %q", header)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started := time.Now()
	s.check(ctx)
	snap := s.snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("the signed-in account must be found by the first look: accounts=%d status=%q", len(snap.Accounts), snap.Status)
	}
	a := snap.Accounts[0]
	t.Logf("found after %s: is the active one: %v; has a name: %v; has an email: %v; plan known: %v; usage known: %v; problem: %q",
		time.Since(started).Round(time.Millisecond), a.UUID == snap.Active, a.Name != "", a.Email != "", a.Plan != "", a.Usage != nil, a.Problem)
	m := buildMenu(snap, time.Now())
	t.Logf("menu: header %q; rows: %d; first row ticked: %v", m.Header, len(m.Accounts), len(m.Accounts) == 1 && m.Accounts[0].Active)
	if a.UUID != snap.Active || a.Email == "" || a.Plan == "" || m.Header != "Account 1 is active" {
		t.Fatal("the first account must come out complete and active")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatalf("the fresh helper saves what it found: %v", err)
	}
	// A second start of the helper reads it back.
	again := newSwitcher(dir)
	if len(again.snapshot().Accounts) != 1 {
		t.Fatal("the saved account is there after a restart")
	}
}
