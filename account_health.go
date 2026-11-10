package main

import (
	"errors"
	"time"
)

// applyVerifiedRefreshLocked stores what one usage check learned. The
// account's details and its numbers are taken together or not at all: a
// profile that names another account or organisation changes nothing.
func (s *switcher) applyVerifiedRefreshLocked(a *Account, p profile, usage *Usage) error {
	if a == nil || s.account(a.UUID) != a || usage == nil {
		return errors.New("account refresh is incomplete")
	}
	if err := a.refreshProfile(p); err != nil {
		return err
	}
	a.Usage, a.Problem = usage, ""
	s.clearRefreshFailureLocked(a.UUID)
	return nil
}

// The usage service may ask to be left alone for a long time; no wait longer
// than this is believed.
const maxRateLimitWait = 8 * 24 * time.Hour

// recordRefreshFailureLocked notes that a usage check failed, and when the
// account may be asked again. The wait grows with each failure in a row and
// survives a restart of the helper. No text from the service is stored.
func (s *switcher) recordRefreshFailureLocked(id string, err error, now time.Time) {
	if s.strikes == nil {
		s.strikes = map[string]int{}
	}
	if s.backoff == nil {
		s.backoff = map[string]time.Time{}
	}
	s.strikes[id] = min(s.strikes[id]+1, 5)
	wait := min(activeEvery<<uint(s.strikes[id]-1), maxBackoff)
	problem := problemUsageUnavailable
	code := "refresh_unavailable"
	var limited *rateLimited
	if errors.As(err, &limited) {
		code = "rate_limited"
		problem = problemUsageRateLimited
		if limited.after > 0 {
			wait = min(limited.after, maxRateLimitWait)
		}
	} else if errors.Is(err, errSignedOut) {
		code = "signed_out"
		problem = problemUsageRefused
	}
	s.backoff[id] = now.Add(wait)
	if a := s.account(id); a != nil {
		a.Problem = problem
		a.RefreshHealth = &AccountRefreshHealth{Version: 1, Code: code, Failures: s.strikes[id], ObservedAt: now.UTC(), RetryAt: s.backoff[id].UTC()}
	}
}

func (s *switcher) clearRefreshFailureLocked(id string) {
	delete(s.strikes, id)
	delete(s.backoff, id)
	if a := s.account(id); a != nil {
		a.RefreshHealth = nil
	}
}

// What the menu says next to an account's numbers when a usage check failed.
// None of these says anything about whether the account can be switched to.
const (
	problemUsageUnavailable = "usage could not be checked just now"
	problemUsageRateLimited = "usage check is being rate-limited"
	problemUsageRefused     = "the service refused this sign-in's usage check"
)

// AccountRefreshHealth preserves endpoint backoff through helper restarts.
// Only successful identity AND usage refresh can clear it. A timer permits a
// fresh check; its expiry never restores eligibility or resets quota numbers.
// No provider error, response, token or sign-in information belongs here.
type AccountRefreshHealth struct {
	Version    int       `json:"version"`
	Code       string    `json:"code"`
	Failures   int       `json:"failures"`
	ObservedAt time.Time `json:"observedAt"`
	RetryAt    time.Time `json:"retryAt"`
}

func validAccountRefreshHealth(h *AccountRefreshHealth, now time.Time) bool {
	if h == nil || h.Version != 1 || h.Failures < 1 || h.Failures > 5 ||
		h.ObservedAt.IsZero() || h.ObservedAt.After(now) || !h.RetryAt.After(h.ObservedAt) {
		return false
	}
	maxWait := maxBackoff
	switch h.Code {
	case "rate_limited":
		maxWait = maxRateLimitWait
	case "refresh_unavailable", "signed_out":
	default:
		return false
	}
	return h.RetryAt.Sub(h.ObservedAt) <= maxWait
}

// Called while constructing the single state owner, or under s.mu. Unknown or
// clock-inconsistent records cannot revive cached eligibility or impose an
// unbounded wait. Keep the failure until an actual refresh resolves it.
func (s *switcher) restoreRefreshHealthLocked(now time.Time) {
	if s.strikes == nil {
		s.strikes = map[string]int{}
	}
	if s.backoff == nil {
		s.backoff = map[string]time.Time{}
	}
	for _, account := range s.st.Accounts {
		if account == nil {
			continue
		}
		if account.RefreshHealth == nil {
			// The label is only ever the readable form of a recorded failure.
			account.Problem = ""
			continue
		}
		h := account.RefreshHealth
		account.Problem = problemUsageUnavailable
		if !validAccountRefreshHealth(h, now) {
			s.strikes[account.UUID] = 1
			s.backoff[account.UUID] = now.Add(maxBackoff)
			continue
		}
		s.strikes[account.UUID], s.backoff[account.UUID] = h.Failures, h.RetryAt
		switch h.Code {
		case "rate_limited":
			account.Problem = problemUsageRateLimited
		case "signed_out":
			account.Problem = problemUsageRefused
		}
	}
}
