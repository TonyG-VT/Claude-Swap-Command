package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// switchFixture is a pretend Mac: a Claude data folder signed in as account
// A, saved sign-ins for B and C, a few conversations, and a Claude that
// "quits" and "starts" when asked. No real sign-in, network or app is touched.
type switchFixture struct {
	t        *testing.T
	s        *switcher
	d        desktop
	root     string
	a, b, c  carryNamespace
	running  bool
	closeErr error
	// reappearOnCheck makes Claude come back by itself: from that closed-check
	// on (counting from 1) it is running again.
	reappearOnCheck int
	checks          int
	closes, starts  int
	onStart         func(f *switchFixture)
	onPoll          func(f *switchFixture, poll int)
	polls           int
	identify        func(account string) (string, error)
	vendorErr       error
	now             time.Time
}

func newSwitchFixture(t *testing.T) *switchFixture { return newSwitchFixtureLaidOut(t, false) }

// newSwitchFixtureLaidOut can use the layout that keeps the cookie files in
// their own Network folder, so a test can stop a write exactly between the
// settings file and the cookies.
func newSwitchFixtureLaidOut(t *testing.T, networkFolder bool) *switchFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if networkFolder {
		for _, name := range []string{"claude", "claude-b", "claude-c"} {
			if err := os.MkdirAll(filepath.Join(root, name, "Network"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	f := &switchFixture{t: t, root: root, d: desktop{dir: filepath.Join(root, "claude")}, running: true, now: time.Unix(1_800_000_000, 0),
		a: carryNamespace{carryID(0xA1), carryID(0xA2)}, b: carryNamespace{carryID(0xB1), carryID(0xB2)}, c: carryNamespace{carryID(0xC1), carryID(0xC2)}}
	helper := filepath.Join(root, "helper")
	// Claude is signed in as A.
	f.signIn(f.d, f.a, "token-a-1")
	write(t, f.d.cookieFiles()[0], "cookies-a")
	// B and C were saved earlier.
	for name, ns := range map[string]carryNamespace{"b": f.b, "c": f.c} {
		other := desktop{dir: filepath.Join(root, "claude-"+name)}
		f.signIn(other, ns, "token-"+name+"-1")
		write(t, other.cookieFiles()[0], "cookies-"+name)
		if err := saveProfile(other, filepath.Join(helper, "accounts", ns.Account)); err != nil {
			t.Fatal(err)
		}
	}
	// Conversations: 1 only on A; 2 on both, newer on A.
	f.record(f.a, 1, nil)
	f.record(f.a, 2, map[string]any{"lastActivityAt": 2000})
	f.record(f.b, 2, map[string]any{"lastActivityAt": 1000})

	f.s = newSwitcher(helper)
	f.s.st.Accounts = []*Account{
		{UUID: f.a.Account, Org: f.a.Org, Name: "Sam", Email: "a@example.test", Plan: "Max"},
		{UUID: f.b.Account, Org: f.b.Org, Name: "Sam", Email: "b@example.test", Plan: "Max"},
		{UUID: f.c.Account, Org: f.c.Org, Name: "Sam B", Email: "c@example.test", Plan: "Max"},
	}
	f.s.active = f.a.Account
	f.s.env = switchEnv{
		desktop: func() desktop { return f.d },
		decrypt: plainCache,
		selfCheck: func(ctx context.Context) selfCheck {
			return runSelfCheck(ctx, selfCheckEnv{desktop: f.d, decrypt: plainCache,
				version:      func() string { return "1.0 (1.0)" },
				verifyVendor: func(context.Context) error { return f.vendorErr },
				cookieStores: func(context.Context, desktop) ([]string, error) { return []string{f.d.cookieFiles()[0]}, nil }})
		},
		close: func(context.Context, func([]string)) error {
			f.closes++
			if f.closeErr != nil {
				return f.closeErr
			}
			f.running = false
			return nil
		},
		closed: func(context.Context) bool {
			f.checks++
			if f.reappearOnCheck > 0 && f.checks >= f.reappearOnCheck {
				f.running = true
			}
			return !f.running
		},
		start: func() error {
			f.starts++
			f.running = true
			if f.onStart != nil {
				f.onStart(f)
			}
			return nil
		},
		identify: func(_ context.Context, account string) (string, error) {
			if f.identify != nil {
				return f.identify(account)
			}
			return account, nil
		},
		sleep: func(_ context.Context, wait time.Duration) error {
			f.now = f.now.Add(wait)
			f.polls++
			if f.onPoll != nil {
				f.onPoll(f, f.polls)
			}
			return nil
		},
		now:           func() time.Time { return f.now },
		claudeVersion: func() string { return "1.0 (1.0)" },
		claudeDir:     func() string { return filepath.Join(root, "dot-claude") },
		verifyPoll:    time.Second, verifyLimit: 90 * time.Second, signedOutGrace: 6 * time.Second, verifySettle: 10 * time.Second,
	}
	// A real Claude renews its tokens soon after it starts.
	f.onStart = renewTokens
	return f
}

func (f *switchFixture) signIn(d desktop, ns carryNamespace, token string) {
	f.t.Helper()
	entries := map[string]*fakeToken{}
	if token != "" {
		entries[fakeCacheKey(ns.Account, ns.Org)] = &fakeToken{Token: token, ExpiresAt: 1}
	}
	cfg := map[string]any{"locale": "en-US", "lastKnownAccountUuid": ns.Account, "oauth:tokenCacheV2": fakeSignInCache(f.t, entries)}
	data, _ := json.Marshal(cfg)
	write(f.t, d.configPath(), string(data))
}

func (f *switchFixture) record(ns carryNamespace, n int, fields map[string]any) string {
	return (&carryFixture{t: f.t, d: f.d}).put(ns, n, fields)
}

func (f *switchFixture) recordPath(ns carryNamespace, n int) string {
	return filepath.Join(f.d.sessionsDir(), ns.Account, ns.Org, "local_"+carryID(n)+".json")
}

func (f *switchFixture) live() signInState {
	f.t.Helper()
	state, err := readSignIn(f.d.configPath(), plainCache)
	if err != nil {
		f.t.Fatalf("live sign-in unreadable: %v", err)
	}
	return state
}

func (f *switchFixture) namespace(account string) carryNamespace {
	for _, ns := range []carryNamespace{f.a, f.b, f.c} {
		if ns.Account == account {
			return ns
		}
	}
	f.t.Fatalf("unknown fixture account")
	return carryNamespace{}
}

// renewTokens is what a healthy Claude does after launch: it keeps the
// account it finds and writes fresh tokens for it.
func renewTokens(f *switchFixture) {
	if state := f.live(); state.Account != "" {
		f.signIn(f.d, f.namespace(state.Account), fmt.Sprintf("token-renewed-%d", f.starts))
	}
}

// dropSignIn is what Claude does with a sign-in it no longer accepts: it
// keeps its settings entries but empties them, and shows its sign-in screen.
func dropSignIn(f *switchFixture) {
	if state := f.live(); state.Account != "" {
		f.signIn(f.d, f.namespace(state.Account), "")
	}
}

func (f *switchFixture) pending() []string {
	f.t.Helper()
	pending, err := pendingBackups(f.d, f.s.backupsDir())
	if err != nil {
		f.t.Fatal(err)
	}
	return pending
}

func (f *switchFixture) journal() []journalEntry {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.s.dir, "journal", "switches.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var entries []journalEntry
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var e journalEntry
		if err := json.Unmarshal(line, &e); err != nil {
			f.t.Fatal(err)
		}
		entries = append(entries, e)
	}
	return entries
}

func (f *switchFixture) savedCookies(ns carryNamespace) string {
	return read(f.t, filepath.Join(f.s.profileDir(ns.Account), "Cookies"))
}

func TestSwitchSignsInTheTargetAndCarriesTheConversations(t *testing.T) {
	f := newSwitchFixture(t)
	oldCopy := read(t, f.recordPath(f.b, 2))
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if f.live().Account != f.b.Account || read(t, f.d.cookieFiles()[0]) != "cookies-b" {
		t.Fatal("Claude must end up signed in as the target, with the target's web session")
	}
	if f.closes != 1 || f.starts != 1 || !f.running {
		t.Fatalf("Claude is closed once and reopened once: closes=%d starts=%d", f.closes, f.starts)
	}
	// The account that was left is saved for the way back.
	if _, err := checkSavedSignIn(f.s.profileDir(f.a.Account), f.a.Account, plainCache); err != nil || f.savedCookies(f.a) != "cookies-a" {
		t.Fatalf("the account being left must be saved: %v", err)
	}
	// Every conversation followed, byte for byte.
	for _, n := range []int{1, 2} {
		if read(t, f.recordPath(f.b, n)) != read(t, f.recordPath(f.a, n)) {
			t.Fatalf("conversation %d did not follow", n)
		}
	}
	if read(t, f.recordPath(f.b, 2)) == oldCopy {
		t.Fatal("the destination's older copy must be replaced by the newer one")
	}
	if len(f.pending()) != 0 {
		t.Fatal("a finished switch leaves nothing pending")
	}
	snap := f.s.snapshot()
	if snap.Active != f.b.Account {
		t.Fatal("the menu must show the target as the active account")
	}
	outcome := f.s.lastOutcome
	if outcome == nil || outcome.From != f.a.Account || outcome.To != f.b.Account || outcome.Verdict != verdictConfirmed || outcome.Carry == nil || len(outcome.Carry.writes) != 2 {
		t.Fatalf("outcome: %+v", outcome)
	}
	entries := f.journal()
	last := entries[len(entries)-1]
	if last.Kind != "manual_switch" || last.Result != "ok" || last.From != f.a.Account || last.To != f.b.Account || last.Verdict != "confirmed" || last.Carry == nil || last.Carry.New != 1 || last.Carry.Updated != 1 || last.Carry.Outcome != "full" {
		t.Fatalf("journal entry: %+v %+v", last, last.Carry)
	}

	// And back again: the newer copies now on B travel back unchanged.
	if err := f.s.switchTo(f.a.Account); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if f.live().Account != f.a.Account || read(t, f.d.cookieFiles()[0]) != "cookies-a" || f.s.snapshot().Active != f.a.Account {
		t.Fatal("the way back must land on the first account")
	}
	if f.s.lastOutcome.Carry == nil || len(f.s.lastOutcome.Carry.writes) != 0 || f.s.lastOutcome.Carry.Identical != 2 {
		t.Fatalf("nothing changed in between, so nothing is rewritten on the way back: %+v", f.s.lastOutcome.Carry)
	}
}

// Eight real switch attempts were once refused only because the usage service
// was rate-limiting. A switch must not need the network at all.
func TestSwitchNeverCallsTheUsageService(t *testing.T) {
	previous := client.Transport
	defer func() { client.Transport = previous }()
	client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a switch made a network request to %s", r.URL.Path)
		return nil, errors.New("fixture: no network")
	})
	f := newSwitchFixture(t)
	f.identify = func(string) (string, error) { return "", &rateLimited{} }
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatalf("a switch must succeed while the service is unavailable: %v", err)
	}
	if f.live().Account != f.b.Account || f.s.lastOutcome.Verdict != verdictConfirmed {
		t.Fatalf("Claude renewing its own tokens is proof enough: %+v", f.s.lastOutcome)
	}
}

func TestSwitchRefusesBeforeClosingClaudeWhenItCannotWork(t *testing.T) {
	for name, tc := range map[string]struct {
		breakIt  func(f *switchFixture)
		wantCode string
	}{
		"no saved sign-in": {func(f *switchFixture) {
			if err := os.RemoveAll(f.s.profileDir(f.b.Account)); err != nil {
				f.t.Fatal(err)
			}
		}, "NO_SAVED_SIGNIN"},
		"saved sign-in belongs to someone else": {func(f *switchFixture) {
			other := desktop{dir: filepath.Join(f.root, "claude-wrong")}
			f.signIn(other, f.c, "token-wrong")
			write(f.t, other.cookieFiles()[0], "cookies-wrong")
			if err := saveProfile(other, f.s.profileDir(f.b.Account)); err != nil {
				f.t.Fatal(err)
			}
		}, "SAVED_SIGNIN_MISMATCH"},
		"Claude.app is not Anthropic's": {func(f *switchFixture) { f.vendorErr = errors.New("fixture: bad signature") }, "CLAUDE_NOT_ANTHROPIC"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSwitchFixture(t)
			tc.breakIt(f)
			before := f.live().Fingerprint
			err := f.s.switchTo(f.b.Account)
			if errorCode(err) != tc.wantCode {
				t.Fatalf("want %s, got %v", tc.wantCode, err)
			}
			if f.closes != 0 || f.starts != 0 || !f.running {
				t.Fatalf("Claude must not even be asked to close: closes=%d starts=%d", f.closes, f.starts)
			}
			if f.live().Fingerprint != before || read(t, f.d.cookieFiles()[0]) != "cookies-a" {
				t.Fatal("nothing may change")
			}
		})
	}
}

func TestSwitchChangesNothingWhenClaudeStaysOpen(t *testing.T) {
	f := newSwitchFixture(t)
	f.closeErr = coded("CLAUDE_STILL_OPEN", "Claude did not close, so nothing was changed.")
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "CLAUDE_STILL_OPEN" {
		t.Fatalf("want CLAUDE_STILL_OPEN, got %v", err)
	}
	if f.live().Account != f.a.Account || f.starts != 0 {
		t.Fatal("nothing may change, and a Claude that is still open is not started again")
	}
	if entries, _ := os.ReadDir(f.s.backupsDir()); len(entries) != 0 {
		t.Fatalf("no backup is even started: %d entries", len(entries))
	}
	if _, err := os.Stat(f.recordPath(f.b, 1)); !os.IsNotExist(err) {
		t.Fatal("no conversation may be copied")
	}
}

// Conversations are a convenience on top of the account switch. A folder the
// helper cannot make sense of must not keep the person on a full account.
func TestSwitchStillHappensWhenConversationsCannotBeCarried(t *testing.T) {
	f := newSwitchFixture(t)
	defer func(limit int) { carryMaxEntries = limit }(carryMaxEntries)
	carryMaxEntries = 8
	for n := 1; n <= carryMaxEntries+1; n++ {
		write(t, filepath.Join(f.d.sessionsDir(), f.a.Account, f.a.Org, fmt.Sprintf("filler-%d", n)), "x")
	}
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatalf("the account must still switch: %v", err)
	}
	if f.live().Account != f.b.Account {
		t.Fatal("Claude must be on the target account")
	}
	outcome := f.s.lastOutcome
	if outcome.Carry != nil || outcome.CarryNote == "" {
		t.Fatalf("the outcome must say that and why conversations were not carried: %+v", outcome)
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Result != "ok" || last.Carry == nil || last.Carry.Outcome != "none" || last.Carry.Reason == "" {
		t.Fatalf("journal: %+v %+v", last, last.Carry)
	}
	if _, err := os.Stat(f.recordPath(f.b, 1)); !os.IsNotExist(err) {
		t.Fatal("no conversation is copied when the folder cannot be understood")
	}
}

func TestSwitchPutsEverythingBackWhenAStepFails(t *testing.T) {
	f := newSwitchFixture(t)
	// The current sign-in cannot be saved: the copy that is made of it does
	// not check out (Claude's settings name a different account than the
	// tokens belong to), so it is thrown away and the switch stops.
	f.onStart = nil
	tokens := map[string]*fakeToken{fakeCacheKey(f.a.Account, f.a.Org): {Token: "token-a-1", ExpiresAt: 1}}
	writeSignInConfig(t, f.d.configPath(), f.c.Account, fakeSignInCache(t, tokens))
	oldCopy := read(t, f.recordPath(f.b, 2))
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "SAVE_SIGNIN_FAILED" || !strings.Contains(err.Error(), "everything was put back") {
		t.Fatalf("want SAVE_SIGNIN_FAILED with the outcome stated, got %v", err)
	}
	if f.live().Account != f.a.Account || read(t, f.d.cookieFiles()[0]) != "cookies-a" {
		t.Fatal("Claude must still be signed in as before")
	}
	if read(t, f.recordPath(f.b, 2)) != oldCopy {
		t.Fatal("conversations copied before the failure must be put back")
	}
	if _, err := os.Stat(f.recordPath(f.b, 1)); !os.IsNotExist(err) {
		t.Fatal("a conversation that was new on the destination must be removed again")
	}
	if f.starts != 1 || !f.running || len(f.pending()) != 0 {
		t.Fatalf("Claude is reopened and nothing is left pending: starts=%d pending=%v", f.starts, f.pending())
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Result != "failed" || last.Code != "SAVE_SIGNIN_FAILED" {
		t.Fatalf("journal: %+v", last)
	}
	if f.s.snapshot().Active != f.a.Account {
		t.Fatal("the menu must still show the first account")
	}
}

// Claude's updater can start Claude again by itself. If that happens in the
// middle, the switch stops without touching anything more, and nothing is
// left pending that could block the next attempt.
func TestSwitchSettlesWhenClaudeReappearsByItself(t *testing.T) {
	f := newSwitchFixture(t)
	// The looks at "is Claude closed": 1 after it quit, 2 before anything is
	// changed, 3 before the sign-in is saved, 4 before the sign-in settings are
	// removed, 5 before the cookies are swapped, 6 before the new settings.
	f.reappearOnCheck = 5 // after the sign-in settings were removed, before the cookies are swapped
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "CLAUDE_REOPENED" {
		t.Fatalf("want CLAUDE_REOPENED, got %v", err)
	}
	if read(t, f.d.cookieFiles()[0]) != "cookies-a" {
		t.Fatal("no file may be written once Claude is running again")
	}
	if state := f.live(); !state.SignedOut() {
		t.Fatalf("on disk there is one account's web session and no tokens, never a mix: %+v", state)
	}
	if f.starts != 0 {
		t.Fatal("a Claude that is already running is not started again")
	}
	if len(f.pending()) != 0 {
		t.Fatal("nothing may be left pending")
	}
	// The next attempt works.
	f.reappearOnCheck, f.checks = 0, 0
	f.signIn(f.d, f.a, "token-a-reminted") // Claude signed itself back in from its web session
	if err := f.s.switchTo(f.b.Account); err != nil || f.live().Account != f.b.Account {
		t.Fatalf("the next attempt must work: %v", err)
	}
}

// A saved sign-in can expire. Claude then shows its sign-in screen. That is
// reported as such, and going back is an ordinary switch.
func TestSwitchReportsASavedSignInClaudeNoLongerAccepts(t *testing.T) {
	f := newSwitchFixture(t)
	f.onStart = dropSignIn
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "SIGNIN_NOT_ACCEPTED" {
		t.Fatalf("want SIGNIN_NOT_ACCEPTED, got %v", err)
	}
	if !strings.Contains(err.Error(), "Account 2") || !strings.Contains(err.Error(), "Account 1") {
		t.Fatalf("the message names the account to sign in to and the one to go back to: %v", err)
	}
	if f.s.lastOutcome.Verdict != verdictNeedsSignIn || f.s.snapshot().Active != "" {
		t.Fatalf("nobody is signed in now: %+v", f.s.lastOutcome)
	}
	if len(f.pending()) != 0 {
		t.Fatal("the switch is closed even though it did not take")
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Verdict != "needs_sign_in" || last.Code != "SIGNIN_NOT_ACCEPTED" {
		t.Fatalf("journal: %+v", last)
	}

	// Go back: from the sign-in screen to the account that was just left.
	f.onStart = renewTokens
	if err := f.s.switchTo(f.a.Account); err != nil {
		t.Fatalf("going back: %v", err)
	}
	if f.live().Account != f.a.Account || read(t, f.d.cookieFiles()[0]) != "cookies-a" || f.s.snapshot().Active != f.a.Account {
		t.Fatal("going back must land on the account that was left")
	}
}

func TestSwitchingToTheAccountThatIsAlreadySignedInDoesNothing(t *testing.T) {
	f := newSwitchFixture(t)
	if err := f.s.switchTo(f.a.Account); err != nil {
		t.Fatal(err)
	}
	if f.closes != 0 || f.starts != 0 {
		t.Fatal("Claude is left alone when it is already on that account")
	}
	if err := f.s.switchTo(carryID(0xDD)); errorCode(err) != "UNKNOWN_ACCOUNT" {
		t.Fatalf("an account that is not set up: %v", err)
	}
}

func TestVerifyAfterLaunchTellsTheFourOutcomesApart(t *testing.T) {
	for name, tc := range map[string]struct {
		onPoll     func(f *switchFixture, poll int)
		identify   func(string) (string, error)
		twoEntries bool
		notRunning bool
		want       switchVerdict
	}{
		"Claude renews the tokens": {
			onPoll: func(f *switchFixture, poll int) {
				if poll == 3 {
					f.signIn(f.d, f.b, "token-b-renewed")
				}
			},
			identify: func(string) (string, error) { return "", errors.New("fixture: offline") },
			want:     verdictConfirmed,
		},
		"the service confirms the account": {
			identify: func(account string) (string, error) { return account, nil },
			want:     verdictConfirmed,
		},
		"offline and nothing renewed": {
			identify: func(string) (string, error) { return "", errors.New("fixture: offline") },
			want:     verdictUnconfirmed,
		},
		"Claude empties the sign-in": {
			onPoll: func(f *switchFixture, poll int) {
				if poll == 2 {
					f.signIn(f.d, f.b, "")
				}
			},
			identify: func(string) (string, error) { return "", errors.New("fixture: offline") },
			want:     verdictNeedsSignIn,
		},
		"another account appears": {
			onPoll: func(f *switchFixture, poll int) {
				if poll == 2 {
					f.signIn(f.d, f.c, "token-c")
				}
			},
			identify: func(string) (string, error) { return "", errors.New("fixture: offline") },
			want:     verdictDifferentAccount,
		},
		// The service knowing the token says nothing about what Claude will do
		// with it. Claude gets time to act before the service's word counts.
		"the service confirms, but Claude then drops the sign-in": {
			onPoll: func(f *switchFixture, poll int) {
				if poll == 3 {
					f.signIn(f.d, f.b, "")
				}
			},
			identify: func(account string) (string, error) { return account, nil },
			want:     verdictNeedsSignIn,
		},
		// Claude throwing away one of the account's entries changes the sign-in,
		// but it is not Claude renewing it.
		"Claude drops one of two entries": {
			twoEntries: true,
			onPoll: func(f *switchFixture, poll int) {
				if poll == 2 {
					f.signIn(f.d, f.b, "token-b-restored")
				}
			},
			identify: func(string) (string, error) { return "", errors.New("fixture: offline") },
			want:     verdictUnconfirmed,
		},
		"the service confirms, but Claude is not running": {
			notRunning: true,
			identify:   func(account string) (string, error) { return account, nil },
			want:       verdictUnconfirmed,
		},
		"the service names another account": {
			identify: func(string) (string, error) { return carryID(0xC1), nil },
			want:     verdictDifferentAccount,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSwitchFixture(t)
			f.signIn(f.d, f.b, "token-b-restored")
			if tc.twoEntries {
				entries := map[string]*fakeToken{
					fakeCacheKey(f.b.Account, f.b.Org):                 {Token: "token-b-restored", ExpiresAt: 1},
					fakeCacheKey(f.b.Account, f.b.Org) + " user:extra": {Token: "token-b-second", ExpiresAt: 1},
				}
				writeSignInConfig(t, f.d.configPath(), f.b.Account, fakeSignInCache(t, entries))
			}
			restored := f.live()
			f.running = !tc.notRunning
			f.onPoll, f.identify = tc.onPoll, tc.identify
			if got := f.s.verifyAfterLaunch(context.Background(), f.b.Account, restored); got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInterruptedSwitchIsClosedWhileClaudeRunsAndOfferedWhenItIsNot(t *testing.T) {
	f := newSwitchFixture(t)
	leavePending := func() {
		bk, err := backup(f.d, f.s.backupsDir(), filepath.Join(f.s.dir, "spaces-seen.json"))
		if err != nil {
			t.Fatal(err)
		}
		mustWriteProtected(t, f.d.cookieFiles()[0], "cookies-half-switched")
		// The helper stopped here: forget the transaction as a crash would.
		backupRegistry.Lock()
		for key, tx := range backupRegistry.active {
			if tx.dir == bk {
				delete(backupRegistry.active, key)
			}
		}
		backupRegistry.Unlock()
	}

	// Claude is running: nothing can be put back underneath it, so the
	// leftover is simply closed.
	leavePending()
	if err := f.s.resolveInterrupted(context.Background()); err != nil || len(f.pending()) != 0 {
		t.Fatalf("with Claude running the leftover is closed: %v %v", err, f.pending())
	}

	// Claude is closed: the person decides.
	write(t, f.d.cookieFiles()[0], "cookies-a")
	f.running = false
	leavePending()
	if err := f.s.resolveInterrupted(context.Background()); errorCode(err) != "INTERRUPTED_SWITCH" {
		t.Fatalf("with Claude closed the choice is offered: %v", err)
	}
	if err := f.s.switchTo(f.b.Account); errorCode(err) != "INTERRUPTED_SWITCH" {
		t.Fatalf("a new switch waits for that choice: %v", err)
	}
	if err := f.s.fixInterruptedSwitch(context.Background(), true); err != nil {
		t.Fatalf("put back: %v", err)
	}
	if read(t, f.d.cookieFiles()[0]) != "cookies-a" || len(f.pending()) != 0 || !f.running {
		t.Fatal("putting back restores the files, clears the leftover and reopens Claude")
	}

	// Keeping things as they are closes the leftover without writing.
	f.running = false
	leavePending()
	if err := f.s.fixInterruptedSwitch(context.Background(), false); err != nil {
		t.Fatalf("keep: %v", err)
	}
	if read(t, f.d.cookieFiles()[0]) != "cookies-half-switched" || len(f.pending()) != 0 {
		t.Fatal("keeping leaves the files and clears the leftover")
	}
}

func TestAddAccountSavesTheCurrentSignInAndOpensClaudeSignedOut(t *testing.T) {
	f := newSwitchFixture(t)
	f.onStart = nil
	if err := f.s.addAccount(); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if _, err := checkSavedSignIn(f.s.profileDir(f.a.Account), f.a.Account, plainCache); err != nil || f.savedCookies(f.a) != "cookies-a" {
		t.Fatalf("the current sign-in is saved first: %v", err)
	}
	if state := f.live(); !state.SignedOut() {
		t.Fatalf("Claude must open on its own sign-in screen: %+v", state)
	}
	if _, err := os.Stat(f.d.cookieFiles()[0]); !os.IsNotExist(err) {
		t.Fatal("the web session is cleared locally (never signed out at the service)")
	}
	if f.closes != 1 || f.starts != 1 || len(f.pending()) != 0 {
		t.Fatalf("closes=%d starts=%d pending=%v", f.closes, f.starts, f.pending())
	}
	if !f.s.adding() {
		t.Fatal("the helper is now waiting for the new sign-in")
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Kind != "add_account" || last.Stage != "reopen_for_sign_in" || last.Result != "ok" || last.From != f.a.Account {
		t.Fatalf("journal: %+v", last)
	}
	// Cancelling is an ordinary switch back to the saved account.
	f.onStart = renewTokens
	if err := f.s.switchTo(f.a.Account); err != nil || f.live().Account != f.a.Account {
		t.Fatalf("switching back from the sign-in screen: %v", err)
	}
	if f.s.adding() {
		t.Fatal("going back to a saved account ends the wait for a new sign-in")
	}
}

func TestAFinishedSwitchClearsOutOldBackups(t *testing.T) {
	f := newSwitchFixture(t)
	var old []string
	for i := 0; i < 11; i++ {
		old = append(old, agedBackup(t, f.d, f.s.backupsDir(), time.Now().Add(-time.Duration(30+i)*24*time.Hour), true))
	}
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatal(err)
	}
	// Twelve finished backups now: this switch's and the nine newest old ones stay.
	for i, bk := range old {
		if exists(bk) != (i < 9) {
			t.Fatalf("old backup %d: kept=%v", i, exists(bk))
		}
	}
	entries, err := os.ReadDir(f.s.backupsDir())
	if err != nil || len(entries) != 10 {
		t.Fatalf("ten backups remain: %d %v", len(entries), err)
	}
}

// If Claude is signed in to an account the helper has never saved, switching
// away would destroy the only copy of that sign-in. The switch is refused and
// everything is put back.
func TestSwitchRefusesToLeaveAnAccountItHasNotSaved(t *testing.T) {
	f := newSwitchFixture(t)
	stranger := carryNamespace{carryID(0xD1), carryID(0xD2)}
	f.onStart = nil
	f.signIn(f.d, stranger, "token-stranger")
	write(t, f.d.cookieFiles()[0], "cookies-stranger")
	before := f.live().Fingerprint
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "ACCOUNT_NOT_SAVED" || !strings.Contains(err.Error(), "everything was put back") {
		t.Fatalf("want ACCOUNT_NOT_SAVED with everything put back, got %v", err)
	}
	if f.live().Fingerprint != before || read(t, f.d.cookieFiles()[0]) != "cookies-stranger" {
		t.Fatal("the unsaved account's sign-in must still be in place")
	}
	if _, err := os.Stat(f.s.profileDir(stranger.Account)); !os.IsNotExist(err) {
		t.Fatal("nothing is saved under an account the helper does not know")
	}
	if f.starts != 1 || !f.running || len(f.pending()) != 0 {
		t.Fatalf("Claude is reopened and nothing is pending: starts=%d", f.starts)
	}
}

func TestSwitchToARetiredAccountIsRefusedBeforeAnythingHappens(t *testing.T) {
	f := newSwitchFixture(t)
	f.s.st.Accounts[1].Retired = true
	if err := f.s.switchTo(f.b.Account); errorCode(err) != "ACCOUNT_RETIRED" {
		t.Fatalf("want ACCOUNT_RETIRED, got %v", err)
	}
	if f.closes != 0 || f.live().Account != f.a.Account {
		t.Fatal("Claude is left alone")
	}
}

func TestSwitchReportsWhenClaudeEndsUpOnADifferentAccount(t *testing.T) {
	f := newSwitchFixture(t)
	// After it starts, Claude is signed in to somebody else entirely.
	f.onStart = func(f *switchFixture) { f.signIn(f.d, f.c, "token-c-surprise") }
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "DIFFERENT_ACCOUNT" {
		t.Fatalf("want DIFFERENT_ACCOUNT, got %v", err)
	}
	if f.s.lastOutcome.Verdict != verdictDifferentAccount || f.s.snapshot().Active != "" || len(f.pending()) != 0 {
		t.Fatalf("the helper claims no account and leaves nothing pending: %+v", f.s.lastOutcome)
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Verdict != "different_account" || last.Code != "DIFFERENT_ACCOUNT" {
		t.Fatalf("journal: %+v", last)
	}
	// The next look at Claude's own sign-in tells the menu who it really is.
	if active, ok := f.s.noticeSignIn(); !ok || active != f.c.Account {
		t.Fatalf("the menu follows what Claude is really signed in to: %q", active)
	}
}

func TestFixInterruptedSwitchLeavesWhatClaudeHasChangedSince(t *testing.T) {
	f := newSwitchFixture(t)
	bk, err := backup(f.d, f.s.backupsDir(), filepath.Join(f.s.dir, "spaces-seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteProtected(t, f.d.cookieFiles()[0], "cookies-half-switched")
	backupRegistry.Lock()
	for key, tx := range backupRegistry.active {
		if tx.dir == bk {
			delete(backupRegistry.active, key)
		}
	}
	backupRegistry.Unlock()
	// Claude ran in between and rewrote the file.
	write(t, f.d.cookieFiles()[0], "cookies-claude-wrote-these")
	f.running = false
	err = f.s.fixInterruptedSwitch(context.Background(), true)
	if errorCode(err) != "PUT_BACK_NOT_POSSIBLE" {
		t.Fatalf("want PUT_BACK_NOT_POSSIBLE, got %v", err)
	}
	if read(t, f.d.cookieFiles()[0]) != "cookies-claude-wrote-these" {
		t.Fatal("a file Claude has changed since is never overwritten")
	}
	if len(f.pending()) != 0 || !f.running {
		t.Fatal("the leftover is cleared and Claude is opened")
	}
}

func TestOnlyOneThingRunsAtATime(t *testing.T) {
	f := newSwitchFixture(t)
	inside, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- f.s.run("First", func() error {
			close(inside)
			<-release
			return coded("STOPPED", "the first one ended")
		})
	}()
	<-inside
	called := false
	if err := f.s.run("Second", func() error { called = true; return nil }); errorCode(err) != "BUSY" || called {
		t.Fatalf("a second action must be turned away while the first runs: %v called=%v", err, called)
	}
	close(release)
	if err := <-first; errorCode(err) != "STOPPED" {
		t.Fatalf("the first action's own result is reported: %v", err)
	}
}

// halfSwitched leaves the fixture as a switch from A to B that was cut short
// right after account B was put in place, with its backup still owed.
func (f *switchFixture) halfSwitched() string {
	f.t.Helper()
	bk, err := backup(f.d, f.s.backupsDir(), filepath.Join(f.s.dir, "spaces-seen.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := restoreProfileSafely(f.d, f.s.profileDir(f.b.Account), func() bool { return true }); err != nil {
		f.t.Fatal(err)
	}
	backupRegistry.Lock()
	for key, tx := range backupRegistry.active {
		if tx.dir == bk {
			delete(backupRegistry.active, key)
		}
	}
	backupRegistry.Unlock()
	f.running = false
	return bk
}

// A put-back that cannot be finished stays owed, and Claude is not opened on
// what is half put back.
func TestFixKeepsAnUnfinishedPutBackOwedAndClaudeClosed(t *testing.T) {
	f := newSwitchFixtureLaidOut(t, true)
	f.halfSwitched()
	unlock := lockFolder(t, filepath.Join(f.d.dir, "Network"))
	err := f.s.fixInterruptedSwitch(context.Background(), true)
	if errorCode(err) != "PUT_BACK_INCOMPLETE" {
		t.Fatalf("want PUT_BACK_INCOMPLETE, got %v", err)
	}
	if f.starts != 0 || f.running {
		t.Fatal("Claude is not opened on a put-back that stopped partway")
	}
	if len(f.pending()) != 1 || !f.s.snapshot().Blocked {
		t.Fatal("the put-back is still owed and the menu still offers the fix")
	}
	if state := f.live(); !state.SignedOut() {
		t.Fatalf("nobody's tokens are on disk meanwhile: %+v", state)
	}
	unlock()
	if err := f.s.fixInterruptedSwitch(context.Background(), true); err != nil {
		t.Fatalf("the second try finishes the put-back: %v", err)
	}
	if f.live().Account != f.a.Account || read(t, f.d.cookieFiles()[0]) != "cookies-a" || len(f.pending()) != 0 || !f.running || f.s.snapshot().Blocked {
		t.Fatal("everything is back, nothing is owed, Claude is open")
	}
}

// When a switch fails and cannot be undone by itself, the message points at
// "Fix an interrupted switch…", so the menu must offer it right away.
func TestASwitchThatCannotBeUndoneOffersTheFixAtOnce(t *testing.T) {
	f := newSwitchFixture(t)
	inner := f.s.env.closed
	locked := false
	f.s.env.closed = func(ctx context.Context) bool {
		closed := inner(ctx)
		// Just before the sign-in is touched: the settings file can no longer be
		// written (the switch fails), and neither can the destination's
		// conversation folder (so the conversations already copied cannot be
		// taken out again).
		if f.checks == 4 && !locked {
			locked = true
			lockFolder(t, f.d.dir)
			lockFolder(t, filepath.Join(f.d.sessionsDir(), f.b.Account, f.b.Org))
		}
		return closed
	}
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "RECOVERY_NEEDED" {
		t.Fatalf("want RECOVERY_NEEDED, got %v", err)
	}
	if f.starts != 0 {
		t.Fatal("Claude is not opened on a switch that could not be undone")
	}
	snap := f.s.snapshot()
	if !snap.Blocked || !buildMenu(snap, f.now).ShowFix {
		t.Fatal("the menu offers Fix an interrupted switch… as the message says")
	}
}

func TestFixCanSetAsideABackupThatCannotBeRead(t *testing.T) {
	f := newSwitchFixture(t)
	bk := f.halfSwitched()
	if err := os.WriteFile(filepath.Join(bk, "manifest.json"), []byte("not a manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.switchTo(f.c.Account); errorCode(err) != "INTERRUPTED_SWITCH" {
		t.Fatalf("a new switch waits: %v", err)
	}
	if err := f.s.fixInterruptedSwitch(context.Background(), true); errorCode(err) != "BACKUP_UNREADABLE" {
		t.Fatalf("putting back is impossible without the backup, and says so: %v", err)
	}
	if err := f.s.fixInterruptedSwitch(context.Background(), false); err != nil {
		t.Fatalf("keeping things as they are sets the unreadable backup aside: %v", err)
	}
	if len(f.pending()) != 0 || exists(bk) || !exists(filepath.Join(f.s.backupsDir(), ".unreadable-"+filepath.Base(bk))) {
		t.Fatal("the backup is kept under another name and nothing is owed")
	}
	f.onStart = renewTokens
	if err := f.s.switchTo(f.c.Account); err != nil {
		t.Fatalf("switching works again: %v", err)
	}
}

// Saving the sign-in of the account being left reads Claude's files. If
// Claude came back in the meantime, those files are moving: nothing is saved.
func TestSwitchDoesNotSaveASignInWhileClaudeIsRunningAgain(t *testing.T) {
	f := newSwitchFixture(t)
	f.reappearOnCheck = 3 // after the conversations were carried, before the sign-in is saved
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "CLAUDE_REOPENED" {
		t.Fatalf("want CLAUDE_REOPENED, got %v", err)
	}
	if exists(f.s.profileDir(f.a.Account)) {
		t.Fatal("no sign-in is saved from a running Claude")
	}
	if f.live().Account != f.a.Account || read(t, f.d.cookieFiles()[0]) != "cookies-a" || len(f.pending()) != 0 {
		t.Fatal("the sign-in is untouched and nothing is owed")
	}
}

// The launcher can report a failure although Claude did start. Nothing can be
// put back under a running Claude, so the switch is closed as it is.
func TestSwitchIsClosedWhenTheLauncherFailsButClaudeIsRunning(t *testing.T) {
	f := newSwitchFixture(t)
	inner := f.s.env.start
	f.s.env.start = func() error {
		_ = inner()
		return errors.New("fixture: the launcher reported a failure")
	}
	err := f.s.switchTo(f.b.Account)
	if errorCode(err) != "LAUNCH_UNCERTAIN" {
		t.Fatalf("want LAUNCH_UNCERTAIN, got %v", err)
	}
	if len(f.pending()) != 0 || f.s.snapshot().Blocked {
		t.Fatal("nothing is left owed")
	}
	if active, ok := f.s.noticeSignIn(); !ok || active != f.b.Account {
		t.Fatalf("the menu follows Claude's own sign-in: %q", active)
	}
}
