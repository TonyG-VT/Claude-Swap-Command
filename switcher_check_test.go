package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkFixture is the switch fixture with sign-ins whose tokens the usage
// check can use, and a pretend usage service.
type checkFixture struct {
	*switchFixture
	mu     sync.Mutex
	calls  []string // "<path> <account>"
	usage  func(account string) (*http.Response, error)
	tokens map[string]carryNamespace
	// orgType is what the service says each account's organisation is.
	orgType map[string]string
}

func jsonResponse(status int, body any, header ...string) *http.Response {
	data, _ := json.Marshal(body)
	res := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(data)))}
	for i := 0; i+1 < len(header); i += 2 {
		res.Header.Set(header[i], header[i+1])
	}
	return res
}

func usageBody(session, weekly float64) map[string]any {
	reset := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	return map[string]any{"five_hour": map[string]any{"utilization": session, "resets_at": reset},
		"seven_day": map[string]any{"utilization": weekly, "resets_at": reset}}
}

func newCheckFixture(t *testing.T) *checkFixture {
	t.Helper()
	f := &checkFixture{switchFixture: newSwitchFixture(t), tokens: map[string]carryNamespace{}}
	f.liveToken(f.d, f.a, "live-a")
	f.savedToken(f.b, "saved-b")
	f.savedToken(f.c, "saved-c")
	f.usage = func(string) (*http.Response, error) { return jsonResponse(http.StatusOK, usageBody(10, 20)), nil }
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		ns, known := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path+" "+ns.Account)
		f.mu.Unlock()
		if !known {
			return jsonResponse(http.StatusUnauthorized, map[string]any{}), nil
		}
		if r.URL.Path == "/api/oauth/profile" {
			return jsonResponse(http.StatusOK, map[string]any{
				"account":      map[string]any{"uuid": ns.Account, "display_name": "Fixture", "email": "fixture@example.test", "has_claude_max": true},
				"organization": map[string]any{"uuid": ns.Org, "organization_type": f.orgTypeOf(ns.Account)}}), nil
		}
		return f.usage(ns.Account)
	})
	return f
}

// liveToken signs a data folder in with a token that has not expired.
func (f *checkFixture) liveToken(d desktop, ns carryNamespace, token string) {
	f.t.Helper()
	f.tokens[token] = ns
	entries := map[string]*fakeToken{fakeCacheKey(ns.Account, ns.Org): {Token: token, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}
	writeSignInConfig(f.t, d.configPath(), ns.Account, fakeSignInCache(f.t, entries))
}

func (f *checkFixture) savedToken(ns carryNamespace, token string) {
	f.t.Helper()
	other := desktop{dir: f.root + "/claude-saved-" + token}
	f.liveToken(other, ns, token)
	write(f.t, other.cookieFiles()[0], "cookies-"+token)
	if err := saveProfile(other, f.s.profileDir(ns.Account)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *checkFixture) account(ns carryNamespace) Account {
	for _, a := range f.s.snapshot().Accounts {
		if a.UUID == ns.Account {
			return a
		}
	}
	f.t.Fatal("account missing from the snapshot")
	return Account{}
}

func (f *checkFixture) called(path string, ns carryNamespace) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == path+" "+ns.Account {
			n++
		}
	}
	return n
}

func TestUsageCheckFillsInEveryAccountAndWhoIsSignedIn(t *testing.T) {
	f := newCheckFixture(t)
	f.s.active = ""
	f.s.check(context.Background())
	snap := f.s.snapshot()
	if snap.Active != f.a.Account {
		t.Fatalf("the signed-in account is read from Claude's own sign-in: %q", snap.Active)
	}
	for _, ns := range []carryNamespace{f.a, f.b, f.c} {
		a := f.account(ns)
		if a.Usage == nil || a.Usage.Session != 10 || a.Usage.Weekly != 20 || a.Problem != "" {
			t.Fatalf("usage for every account, the saved ones too: %+v %q", a.Usage, a.Problem)
		}
	}
}

// Claude keeps lastKnownAccountUuid after it has logged an account out.
func TestUsageCheckDoesNotTakeTheLastKnownAccountForASignIn(t *testing.T) {
	f := newCheckFixture(t)
	f.signIn(f.d, f.a, "") // the entries are emptied; the last account is still named
	f.s.check(context.Background())
	if got := f.s.snapshot().Active; got != "" {
		t.Fatalf("Claude is signed out, so no account is active: %q", got)
	}
}

func TestUsageCheckKeepsTheNumbersWhenTheServiceRefuses(t *testing.T) {
	for name, header := range map[string]string{
		"seconds":   "600",
		"http date": time.Now().Add(10 * time.Minute).UTC().Format(http.TimeFormat),
	} {
		t.Run(name, func(t *testing.T) {
			f := newCheckFixture(t)
			f.s.check(context.Background()) // first, a good reading
			f.usage = func(account string) (*http.Response, error) {
				if account == f.a.Account {
					return jsonResponse(http.StatusTooManyRequests, map[string]any{}, "Retry-After", header), nil
				}
				return jsonResponse(http.StatusOK, usageBody(10, 20)), nil
			}
			f.s.mu.Lock()
			f.s.account(f.a.Account).Usage.CheckedAt = time.Now().Add(-time.Hour) // due again
			f.s.mu.Unlock()
			f.s.check(context.Background())
			a := f.account(f.a)
			if a.Usage == nil || a.Usage.Session != 10 {
				t.Fatalf("the last numbers stay: %+v", a.Usage)
			}
			if a.Problem != "usage check is being rate-limited" {
				t.Fatalf("problem = %q", a.Problem)
			}
			f.s.mu.Lock()
			wait := time.Until(f.s.backoff[f.a.Account])
			f.s.mu.Unlock()
			if wait < 9*time.Minute || wait > 11*time.Minute {
				t.Fatalf("the service's own wait is honoured: %v", wait)
			}
			before := f.called("/api/oauth/usage", f.a)
			f.s.check(context.Background())
			if f.called("/api/oauth/usage", f.a) != before {
				t.Fatal("the account is not asked again before the wait is over")
			}
		})
	}
}

// A saved sign-in's token runs out after a while. That says nothing about the
// account: Claude renews it when the account is switched to.
func TestUsageCheckDoesNotCallAnExpiredSavedSignInSignedOut(t *testing.T) {
	f := newCheckFixture(t)
	f.s.check(context.Background())
	expired := map[string]*fakeToken{fakeCacheKey(f.b.Account, f.b.Org): {Token: "saved-b", ExpiresAt: time.Now().Add(-time.Hour).UnixMilli()}}
	other := desktop{dir: f.root + "/claude-expired"}
	writeSignInConfig(t, other.configPath(), f.b.Account, fakeSignInCache(t, expired))
	write(t, other.cookieFiles()[0], "cookies-b")
	if err := saveProfile(other, f.s.profileDir(f.b.Account)); err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	f.s.account(f.b.Account).Usage.CheckedAt = time.Now().Add(-time.Hour)
	f.s.mu.Unlock()
	before := f.called("/api/oauth/profile", f.b)
	f.s.check(context.Background())
	b := f.account(f.b)
	if b.Problem != "" || b.Usage == nil || b.RefreshHealth != nil {
		t.Fatalf("an expired saved token is not a problem with the account: %q %+v", b.Problem, b.RefreshHealth)
	}
	if f.called("/api/oauth/profile", f.b) != before {
		t.Fatal("the service is not asked with an expired token")
	}
	if detail := usageDetail(b, defaultThreshold, time.Now()); !strings.Contains(detail, "checked 1 hour ago") {
		t.Fatalf("the menu says how old the numbers are: %q", detail)
	}
}

// A switch must not wait for a usage check that is in flight, and a check
// that was cut short says nothing about the account.
func TestASwitchCancelsAUsageCheckInFlight(t *testing.T) {
	f := newCheckFixture(t)
	entered := make(chan struct{})
	var once sync.Once
	f.usage = func(string) (*http.Response, error) { return jsonResponse(http.StatusOK, usageBody(10, 20)), nil }
	inner := client.Transport
	hang := true
	client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		blocking := hang
		f.mu.Unlock()
		if !blocking {
			return inner.RoundTrip(r)
		}
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	checked := make(chan struct{})
	go func() {
		f.s.check(context.Background())
		close(checked)
	}()
	<-entered
	started := time.Now()
	ran := false
	err := f.s.run("Switch account", func() error {
		ran = true
		f.mu.Lock()
		hang = false
		f.mu.Unlock()
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("the switch must run: %v", err)
	}
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("the switch waited %v for the usage check", waited)
	}
	<-checked
	for _, ns := range []carryNamespace{f.a, f.b, f.c} {
		if a := f.account(ns); a.Problem != "" || a.RefreshHealth != nil {
			t.Fatalf("a check that was cut short blames no account: %q", a.Problem)
		}
	}
}

func TestSigningInAfterAddAccountEnrolsTheNewAccount(t *testing.T) {
	f := newCheckFixture(t)
	var told []string
	f.s.notify = func(title, body string) { told = append(told, title+": "+body) }
	f.onStart = nil
	if err := f.s.addAccount(); err != nil {
		t.Fatal(err)
	}
	f.s.check(context.Background())
	if !f.s.adding() || f.s.snapshot().Active != "" {
		t.Fatal("nobody has signed in yet: the helper keeps waiting")
	}
	// The person signs in to a fourth account in Claude.
	fourth := carryNamespace{carryID(0xD1), carryID(0xD2)}
	f.liveToken(f.d, fourth, "live-d")
	write(t, f.d.cookieFiles()[0], "cookies-d")
	f.s.check(context.Background())
	snap := f.s.snapshot()
	if len(snap.Accounts) != 4 || snap.Active != fourth.Account || f.s.adding() {
		t.Fatalf("the new account is added and the wait is over: accounts=%d active=%q adding=%v", len(snap.Accounts), snap.Active, f.s.adding())
	}
	added := snap.Accounts[3]
	if added.UUID != fourth.Account || added.Org != fourth.Org || added.Email != "fixture@example.test" || added.Plan != "Max" {
		t.Fatalf("the new account's details come from the service: %+v", added)
	}
	if len(told) != 1 || !strings.Contains(told[0], "Account 4") {
		t.Fatalf("the person is told once: %v", told)
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Kind != "add_account" || last.Stage != "signed_in" || last.To != fourth.Account || last.Result != "ok" {
		t.Fatalf("journal: %+v", last)
	}
	// And it can be left again like any other account.
	f.onStart = renewTokens
	if err := f.s.switchTo(f.a.Account); err != nil {
		t.Fatalf("switching away from the new account: %v", err)
	}
	if _, err := checkSavedSignIn(f.s.profileDir(fourth.Account), fourth.Account, plainCache); err != nil {
		t.Fatalf("the new account's sign-in is saved when it is left: %v", err)
	}
}

func TestSelfTestRunsWhenClaudeChangesAndWarnsOnlyWhenSwitchingIsPaused(t *testing.T) {
	f := newCheckFixture(t)
	var told []string
	f.s.notify = func(title, body string) { told = append(told, title+": "+body) }
	claude := "1.0 (1.0)"
	runs := 0
	inner := f.s.env.selfCheck
	f.s.env.selfCheck = func(ctx context.Context) selfCheck { runs++; return inner(ctx) }
	f.s.env.claudeVersion = func() string { return claude }

	f.s.watchClaude(context.Background())
	f.s.watchClaude(context.Background())
	if runs != 1 || len(told) != 0 {
		t.Fatalf("the self-test runs once per Claude version and stays quiet when it passes: runs=%d told=%v", runs, told)
	}
	// Claude updates itself, and the new version no longer passes.
	claude = "2.0 (2.0)"
	f.vendorErr = errors.New("fixture: signature changed")
	f.s.watchClaude(context.Background())
	f.s.watchClaude(context.Background())
	if runs != 2 || len(told) != 1 || !strings.Contains(told[0], "paused") {
		t.Fatalf("the person hears about it once, when Claude changes: runs=%d told=%v", runs, told)
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Kind != "self_check" || last.Result != "failed" || last.Code != "CLAUDE_NOT_ANTHROPIC" {
		t.Fatalf("journal: %+v", last)
	}
}

// "Pause usage checks" stops asking the service. It does not stop the helper
// from knowing who is signed in, and it does not stop switching.
func TestPausedUsageChecksAskTheServiceNothingButStillSeeWhoIsSignedIn(t *testing.T) {
	f := newCheckFixture(t)
	if err := f.s.setUsageChecksPaused(true); err != nil {
		t.Fatal(err)
	}
	f.s.active = ""
	f.s.check(context.Background())
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("no request is made while paused: %d", calls)
	}
	if got := f.s.snapshot().Active; got != f.a.Account {
		t.Fatalf("who is signed in is still noticed: %q", got)
	}
	f.onStart = renewTokens
	if err := f.s.switchTo(f.b.Account); err != nil {
		t.Fatalf("switching still works while usage checks are paused: %v", err)
	}
}

func TestSigningInToAKnownAccountAfterAddAccountJustEndsTheWait(t *testing.T) {
	f := newCheckFixture(t)
	var told []string
	f.s.notify = func(title, body string) { told = append(told, title+": "+body) }
	f.onStart = nil
	if err := f.s.addAccount(); err != nil {
		t.Fatal(err)
	}
	// The person signs in to an account that is already in the menu.
	f.liveToken(f.d, f.b, "live-b-again")
	write(t, f.d.cookieFiles()[0], "cookies-b-again")
	f.s.check(context.Background())
	snap := f.s.snapshot()
	if len(snap.Accounts) != 3 || snap.Active != f.b.Account || f.s.adding() {
		t.Fatalf("no account is added twice and the wait is over: accounts=%d active=%q", len(snap.Accounts), snap.Active)
	}
	if len(told) != 1 || !strings.HasPrefix(told[0], "Signed in: Account 2") {
		t.Fatalf("the message says what happened: %v", told)
	}
}

// The log is a file on disk that gets copied into reports. It names accounts
// by number, never by name or email address.
func TestTheLogNamesAccountsByNumberOnly(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)
	f := newCheckFixture(t)
	f.onStart = nil
	if err := f.s.addAccount(); err != nil {
		t.Fatal(err)
	}
	fourth := carryNamespace{carryID(0xD1), carryID(0xD2)}
	f.liveToken(f.d, fourth, "live-d")
	write(t, f.d.cookieFiles()[0], "cookies-d")
	f.s.check(context.Background())
	f.onStart = renewTokens
	if err := f.s.switchTo(f.a.Account); err != nil {
		t.Fatal(err)
	}
	title, body := f.s.switchMessage()
	f.s.tell(title, body, title)
	text := logged.String()
	if !strings.Contains(text, "Account 4") {
		t.Fatalf("the log says which account was added, by number:\n%s", text)
	}
	for _, private := range []string{"@", "Fixture", "example.test"} {
		if strings.Contains(text, private) {
			t.Fatalf("the log must not contain %q:\n%s", private, text)
		}
	}
}

func (f *checkFixture) orgTypeOf(account string) string {
	if kind := f.orgType[account]; kind != "" {
		return kind
	}
	return "claude_max"
}

// The helper is for any of your own accounts, whatever plan they are on. A
// Team or Enterprise account is not "a sign-in that does not match".
func TestUsageCheckAcceptsAccountsOnAnyPlan(t *testing.T) {
	f := newCheckFixture(t)
	f.orgType = map[string]string{f.b.Account: "claude_team", f.c.Account: "claude_enterprise"}
	f.s.check(context.Background())
	for ns, plan := range map[carryNamespace]string{f.a: "Max", f.b: "Team", f.c: "Enterprise"} {
		a := f.account(ns)
		if a.Problem != "" || a.Usage == nil || a.Plan != plan {
			t.Errorf("plan %s: problem=%q usage known=%v plan shown=%q", plan, a.Problem, a.Usage != nil, a.Plan)
		}
	}
}
