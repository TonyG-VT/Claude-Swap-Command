package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selfCheckFixture is a pretend Claude data folder, signed in as account A,
// with a healthy environment that individual tests then break.
type selfCheckFixture struct {
	t       *testing.T
	f       *carryFixture
	env     selfCheckEnv
	account string
	org     string
}

func newSelfCheckFixture(t *testing.T) *selfCheckFixture {
	t.Helper()
	f := newCarryFixture(t)
	s := &selfCheckFixture{t: t, f: f, account: f.src.Account, org: f.src.Org}
	s.signIn(map[string]*fakeToken{fakeCacheKey(s.account, s.org): {Token: "fixture-token", ExpiresAt: 1}})
	write(t, f.d.cookieFiles()[0], "SQLite format 3\x00fixture")
	f.put(f.src, 1, nil)
	s.env = selfCheckEnv{
		desktop:      f.d,
		decrypt:      plainCache,
		version:      func() string { return "99.1.2 (99.1.2)" },
		verifyVendor: func(context.Context) error { return nil },
		cookieStores: func(context.Context, desktop) ([]string, error) { return []string{f.d.cookieFiles()[0]}, nil },
	}
	return s
}

func (s *selfCheckFixture) signIn(entries map[string]*fakeToken) {
	writeSignInConfig(s.t, s.f.d.configPath(), s.account, fakeSignInCache(s.t, entries))
}

// The version number is never part of the decision: a Claude this helper has
// never seen passes as long as the things the helper relies on are there.
func TestSelfCheckPassesOnAClaudeVersionItHasNeverSeen(t *testing.T) {
	s := newSelfCheckFixture(t)
	check := runSelfCheck(context.Background(), s.env)
	if !check.CanSwitch || !check.CanCarry || check.blocker() != nil {
		t.Fatalf("a healthy setup must pass whatever the version: %+v", check)
	}
	if check.SignIn.Account != s.account || check.ClaudeVersion != "99.1.2 (99.1.2)" || check.At.IsZero() {
		t.Fatalf("the check reports who is signed in and which Claude it saw: %+v", check)
	}
	if got := check.summary(); !strings.Contains(got, "99.1.2") || !strings.Contains(got, "passed") {
		t.Fatalf("summary = %q", got)
	}
	for _, probe := range check.Probes {
		if !probe.OK || probe.Detail == "" {
			t.Fatalf("every probe explains itself: %+v", probe)
		}
	}
}

func TestSelfCheckTreatsSignedOutAsAValidState(t *testing.T) {
	s := newSelfCheckFixture(t)
	s.signIn(map[string]*fakeToken{})
	// With nobody signed in there is no session cookie and no folder to carry from.
	s.env.cookieStores = func(context.Context, desktop) ([]string, error) { return nil, nil }
	check := runSelfCheck(context.Background(), s.env)
	if !check.CanSwitch || check.blocker() != nil || !check.SignIn.SignedOut() {
		t.Fatalf("Claude at its sign-in screen must still be switchable: %+v", check)
	}
}

func TestSelfCheckBlocksOnlyWhenSwitchingWouldDoDamage(t *testing.T) {
	other, otherOrg := carryID(0xB1), carryID(0xB2)
	for name, tc := range map[string]struct {
		breakIt  func(s *selfCheckFixture)
		wantCode string
	}{
		"not Anthropic's Claude": {func(s *selfCheckFixture) {
			s.env.verifyVendor = func(context.Context) error { return errors.New("fixture: signature mismatch") }
		}, "CLAUDE_NOT_ANTHROPIC"},
		"sign-in unreadable": {func(s *selfCheckFixture) {
			writeSignInConfig(s.t, s.f.d.configPath(), s.account, "not base64 !!!")
		}, "SIGNIN_FORMAT_CHANGED"},
		"keychain refused": {func(s *selfCheckFixture) {
			s.env.decrypt = func([]byte) ([]byte, error) { return nil, errKeychainUnavailable }
		}, "KEYCHAIN_UNAVAILABLE"},
		"two accounts at once": {func(s *selfCheckFixture) {
			s.signIn(map[string]*fakeToken{fakeCacheKey(s.account, s.org): {Token: "one"}, fakeCacheKey(other, otherOrg): {Token: "two"}})
		}, "MULTIPLE_ACCOUNTS_SIGNED_IN"},
		"session cookie lives somewhere else": {func(s *selfCheckFixture) {
			moved := filepath.Join(s.f.d.dir, "Partitions", "main", "Cookies")
			s.env.cookieStores = func(context.Context, desktop) ([]string, error) { return []string{moved}, nil }
		}, "SIGNIN_COOKIES_MOVED"},
		"cookie file missing": {func(s *selfCheckFixture) {
			if err := os.Remove(s.f.d.cookieFiles()[0]); err != nil {
				s.t.Fatal(err)
			}
		}, "SIGNIN_COOKIES_MISSING"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSelfCheckFixture(t)
			tc.breakIt(s)
			check := runSelfCheck(context.Background(), s.env)
			err := check.blocker()
			if check.CanSwitch || errorCode(err) != tc.wantCode {
				t.Fatalf("want a block with %s, got canSwitch=%v code=%s (%v)", tc.wantCode, check.CanSwitch, errorCode(err), err)
			}
			if strings.Contains(check.summary(), "passed") || sanitizeError(err) == "an unexpected problem" {
				t.Fatalf("a block must say what is wrong in plain words: %q / %v", check.summary(), err)
			}
		})
	}
}

func TestSelfCheckConversationAndProbeProblemsNeverBlockTheAccount(t *testing.T) {
	t.Run("conversation folder cannot be understood", func(t *testing.T) {
		s := newSelfCheckFixture(t)
		for n := 1; n <= carryMaxEntries+1; n++ {
			write(t, filepath.Join(s.f.dir(s.f.src), fmt.Sprintf("filler-%d", n)), "x")
		}
		check := runSelfCheck(context.Background(), s.env)
		if !check.CanSwitch || check.CanCarry || check.blocker() != nil {
			t.Fatalf("the account still switches; only the carry-over is off: %+v", check)
		}
		if !strings.Contains(check.summary(), "conversations") {
			t.Fatalf("the summary says conversations will not be carried: %q", check.summary())
		}
	})
	t.Run("cookie probe unavailable", func(t *testing.T) {
		s := newSelfCheckFixture(t)
		s.env.cookieStores = func(context.Context, desktop) ([]string, error) { return nil, errors.New("fixture: sqlite3 missing") }
		if check := runSelfCheck(context.Background(), s.env); !check.CanSwitch || !check.CanCarry {
			t.Fatalf("a probe that cannot run is not a reason to refuse: %+v", check)
		}
	})
	t.Run("session cookie not found by name", func(t *testing.T) {
		s := newSelfCheckFixture(t)
		s.env.cookieStores = func(context.Context, desktop) ([]string, error) { return nil, nil }
		if check := runSelfCheck(context.Background(), s.env); !check.CanSwitch {
			t.Fatalf("a renamed cookie is not proof of danger; the switch is verified afterwards: %+v", check)
		}
	})
}
