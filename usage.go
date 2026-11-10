// Modified for Claude Swap Command (2026).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const apiBase = "https://api.anthropic.com"

// Usage is an account's plan usage in percent, as the app shows it.
type Usage struct {
	Session      float64   `json:"session"`
	SessionReset time.Time `json:"sessionReset"`
	Weekly       float64   `json:"weekly"`
	WeeklyReset  time.Time `json:"weeklyReset"`
	CheckedAt    time.Time `json:"checkedAt"`
	// Known distinguishes a measured zero from a number the service did not give.
	SessionKnown bool `json:"sessionKnown"`
	WeeklyKnown  bool `json:"weeklyKnown"`
	// SessionIdle: the service said no 5-hour window is running (the account
	// is not in use). For display only; it never counts as a measured number.
	SessionIdle bool `json:"sessionIdle,omitempty"`
}

// UsageWindow is one limit as the service reports it. Known distinguishes a
// measured zero from absent, null or invalid data. Unspecified is a window
// the service sent as null; Idle is one with nothing used and no reset time
// yet. Both mean the window is not running.
type UsageWindow struct {
	Utilization float64
	ResetsAt    time.Time
	Known       bool
	Unspecified bool
	Idle        bool
}

// peak is the fuller of the two limits.
func (u Usage) peak() float64 { return max(u.Session, u.Weekly) }

// parseUsage reads the two limits the menu shows. It does not turn missing
// or null fields into measured zeroes.
func parseUsage(data []byte, checkedAt time.Time) (*Usage, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil || body == nil {
		return nil, errors.New("invalid usage response")
	}
	u := &Usage{CheckedAt: checkedAt}
	if raw, ok := body["five_hour"]; ok {
		window := parseUsageWindow(raw)
		u.Session, u.SessionReset, u.SessionKnown, u.SessionIdle = window.Utilization, window.ResetsAt, window.Known, window.Unspecified || window.Idle
	}
	if raw, ok := body["seven_day"]; ok {
		window := parseUsageWindow(raw)
		u.Weekly, u.WeeklyReset, u.WeeklyKnown = window.Utilization, window.ResetsAt, window.Known
	}
	return u, nil
}

func parseUsageWindow(raw json.RawMessage) UsageWindow {
	if strings.TrimSpace(string(raw)) == "null" {
		return UsageWindow{Unspecified: true}
	}
	var body struct {
		Utilization *float64        `json:"utilization"`
		ResetsAt    json.RawMessage `json:"resets_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Utilization == nil {
		return UsageWindow{}
	}
	reset, ok := parseUsageReset(body.ResetsAt)
	if !ok && *body.Utilization == 0 && (len(body.ResetsAt) == 0 || strings.TrimSpace(string(body.ResetsAt)) == "null") {
		return UsageWindow{Idle: true}
	}
	if !ok || *body.Utilization < 0 || *body.Utilization > 100 {
		return UsageWindow{}
	}
	return UsageWindow{Utilization: *body.Utilization, ResetsAt: reset, Known: true}
}

// The reviewed Desktop plan-usage adapter accepts RFC3339 text or numeric Unix
// seconds. Do not guess milliseconds or convert a missing reset into capacity.
func parseUsageReset(raw json.RawMessage) (time.Time, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		at, err := time.Parse(time.RFC3339Nano, text)
		return at, err == nil && !at.IsZero()
	}
	var seconds float64
	if json.Unmarshal(raw, &seconds) != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= 253402300800 {
		return time.Time{}, false
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*1e9)).UTC(), true
}

type profile struct {
	Account struct {
		UUID        string `json:"uuid"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		HasMax      bool   `json:"has_claude_max"`
		HasPro      bool   `json:"has_claude_pro"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
		Type string `json:"organization_type"`
	} `json:"organization"`
}

// plan names the subscription. The organization type is current; the
// account flags are only a fallback.
func (p profile) plan() string {
	switch t := strings.TrimPrefix(p.Organization.Type, "claude_"); {
	case t == "max" || t == "pro" || t == "team" || t == "enterprise":
		return strings.ToUpper(t[:1]) + t[1:]
	case p.Account.HasMax:
		return "Max"
	case p.Account.HasPro:
		return "Pro"
	}
	return ""
}

var client = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("service redirects are not allowed") },
}

func apiGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-account-switcher/"+version)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		const maxResponse = 1 << 20
		data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
		if err != nil {
			return errors.New("could not read service response")
		}
		if len(data) > maxResponse {
			return errors.New("service response exceeded size limit")
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("invalid service response")
		}
		return nil
	case http.StatusUnauthorized:
		return errSignedOut
	case http.StatusTooManyRequests:
		return &rateLimited{after: retryAfter(res.Header.Get("Retry-After"), time.Now())}
	}
	return fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
}

var errSignedOut = errors.New("sign-in expired")

// retryAfter reads a Retry-After header, which is either a number of seconds
// or a date. Zero means the service gave no usable wait.
func retryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// rateLimited is a 429. after is the server's Retry-After, zero when it gave none.
type rateLimited struct{ after time.Duration }

func (e *rateLimited) Error() string { return "rate limited" }

func fetchProfile(ctx context.Context, token string) (profile, error) {
	var p profile
	err := apiGet(ctx, token, "/api/oauth/profile", &p)
	return p, err
}

func fetchUsage(ctx context.Context, token string) (*Usage, error) {
	var body json.RawMessage
	if err := apiGet(ctx, token, "/api/oauth/usage", &body); err != nil {
		return nil, err
	}
	return parseUsage(body, time.Now())
}

// accessToken decrypts a saved oauth:tokenCacheV2 value and returns an
// unexpired token of the account that may read its profile and usage.
// Cache keys look like "acct:<account>|<client>:<org>:<audience>:<scopes>".
func accessToken(cacheValue, account string, decrypt cacheDecrypter) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cacheValue)
	if err != nil {
		return "", err
	}
	plain, err := decrypt(data)
	if err != nil {
		return "", err
	}
	var cache map[string]struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(plain, &cache); err != nil {
		return "", err
	}
	best, bestExp := "", int64(0)
	now := time.Now().UnixMilli()
	for k, v := range cache {
		if !strings.HasPrefix(k, "acct:"+account+"|") || !strings.Contains(k, "user:profile") {
			continue
		}
		if v.ExpiresAt > now && v.ExpiresAt > bestExp {
			best, bestExp = v.Token, v.ExpiresAt
		}
	}
	if best == "" {
		return "", errSignedOut
	}
	return best, nil
}
