package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var healthTestAccount, healthTestOrg = carryID(0xA1), carryID(0xA2)

func TestAccountRefreshHealthRestartPreservesBackoff(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	for name, refreshError := range map[string]error{
		"service":        errors.New("PRIVATE_PROVIDER_RESPONSE"),
		"auth":           errSignedOut,
		"endpoint limit": &rateLimited{after: 6 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := newSwitcher(dir)
			s.st.Accounts = []*Account{{UUID: healthTestAccount, Org: healthTestOrg}}
			s.recordRefreshFailureLocked(healthTestAccount, refreshError, now)
			s.recordRefreshFailureLocked(healthTestAccount, refreshError, now)
			before := *s.st.Accounts[0].RefreshHealth
			s.saveState()
			data, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var safe map[string]any
			if json.Unmarshal(data, &safe) != nil {
				t.Fatal("invalid saved state")
			}
			if string(data) == "" || bytes.Contains(data, []byte("PRIVATE_PROVIDER_RESPONSE")) {
				t.Fatal("provider data escaped health record")
			}
			restarted := newSwitcher(dir)
			after := restarted.st.Accounts[0].RefreshHealth
			if after == nil || *after != before || restarted.strikes[healthTestAccount] != 2 ||
				!restarted.backoff[healthTestAccount].Equal(before.RetryAt) || restarted.st.Accounts[0].Problem == "" {
				t.Fatal("restart lost the refresh failure or reset its timer")
			}
			restarted.clearRefreshFailureLocked(healthTestAccount)
			if restarted.st.Accounts[0].RefreshHealth != nil || !restarted.backoff[healthTestAccount].IsZero() || restarted.strikes[healthTestAccount] != 0 {
				t.Fatal("successful refresh did not clear failure tracking")
			}
		})
	}
}

// When the wait is over the account may be asked again. That alone clears
// nothing: the note of the failure stays until a check really succeeds.
func TestAccountRefreshHealthExpiryRequiresActualVerification(t *testing.T) {
	now := time.Now().UTC()
	a := &Account{UUID: healthTestAccount, Org: healthTestOrg}
	s := &switcher{st: State{Accounts: []*Account{a}}, backoff: map[string]time.Time{}, strikes: map[string]int{}}
	s.recordRefreshFailureLocked(a.UUID, errors.New("unavailable"), now.Add(-time.Hour))
	a.Problem = "" // Removing the label cannot clear the recorded failure.
	s.restoreRefreshHealthLocked(now)
	if s.backoff[a.UUID].After(now) {
		t.Fatal("a wait that is over was extended instead of permitting a new check")
	}
	if a.Problem == "" || a.RefreshHealth == nil {
		t.Fatal("the end of the wait cleared an unresolved failure")
	}
	s.clearRefreshFailureLocked(a.UUID)
	if a.RefreshHealth != nil || s.strikes[a.UUID] != 0 {
		t.Fatal("a successful check must clear the failure")
	}
}

func TestAccountRefreshHealthMalformedRecordsStayBoundedAndIneligible(t *testing.T) {
	now := time.Now().UTC()
	valid := AccountRefreshHealth{Version: 1, Code: "rate_limited", Failures: 2, ObservedAt: now.Add(-time.Minute), RetryAt: now.Add(time.Minute)}
	mutations := []func(*AccountRefreshHealth){
		func(h *AccountRefreshHealth) { h.Version = 2 },
		func(h *AccountRefreshHealth) { h.Code = "unknown" },
		func(h *AccountRefreshHealth) { h.Failures = 0 },
		func(h *AccountRefreshHealth) { h.Failures = 6 },
		func(h *AccountRefreshHealth) { h.ObservedAt = now.Add(time.Hour) },
		func(h *AccountRefreshHealth) { h.RetryAt = h.ObservedAt },
		func(h *AccountRefreshHealth) { h.RetryAt = now.Add(9 * 24 * time.Hour) },
		func(h *AccountRefreshHealth) { h.Code = "signed_out"; h.RetryAt = now.Add(time.Hour) },
	}
	for _, change := range mutations {
		h := valid
		change(&h)
		a := &Account{UUID: healthTestAccount, RefreshHealth: &h}
		s := &switcher{st: State{Accounts: []*Account{a}}, backoff: map[string]time.Time{}, strikes: map[string]int{}}
		s.restoreRefreshHealthLocked(now)
		if a.Problem == "" || a.RefreshHealth == nil || !s.backoff[a.UUID].Equal(now.Add(maxBackoff)) {
			t.Fatal("malformed health revived eligibility or imposed unbounded wait")
		}
	}
}

func TestAccountRefreshHealthRequiresMatchingIdentityAndUsageToClear(t *testing.T) {
	for _, kind := range []string{"organization", "missing usage", "valid"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			a, p := profileUpgradeFixture()
			s := &switcher{st: State{Accounts: []*Account{&a}}}
			s.recordRefreshFailureLocked(a.UUID, errSignedOut, now.Add(-time.Minute))
			u := &Usage{CheckedAt: now}
			switch kind {
			case "organization":
				p.Organization.UUID = "changed"
			case "missing usage":
				u = nil
			}
			err := s.applyVerifiedRefreshLocked(&a, p, u)
			if kind == "valid" {
				if err != nil || a.RefreshHealth != nil || a.Problem != "" || a.Usage != u {
					t.Fatal("verified refresh did not resolve health")
				}
			} else if err == nil || a.RefreshHealth == nil || a.Problem == "" || a.Usage != nil || s.backoff[a.UUID].IsZero() {
				t.Fatal("unverified identity or missing usage cleared failure tracking")
			}
		})
	}
}
