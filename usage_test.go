package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var policyNow = time.Date(2026, time.October, 26, 12, 0, 0, 0, time.UTC)

func usageFixture() map[string]any {
	window := func(pct float64, d time.Duration) map[string]any {
		return map[string]any{"utilization": pct, "resets_at": policyNow.Add(d).Format(time.RFC3339)}
	}
	return map[string]any{
		"five_hour":        window(20, 2*time.Hour),
		"seven_day":        window(30, 48*time.Hour),
		"seven_day_opus":   window(40, 48*time.Hour),
		"seven_day_sonnet": window(99, 48*time.Hour),
		"extra_usage":      map[string]any{"is_enabled": false},
	}
}

func parseFixture(t *testing.T, body map[string]any) *Usage {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	u, err := parseUsage(data, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsageReadsTheTwoLimitsTheMenuShows(t *testing.T) {
	u := parseFixture(t, usageFixture())
	if !u.SessionKnown || u.Session != 20 || !u.SessionReset.Equal(policyNow.Add(2*time.Hour)) {
		t.Fatalf("5-hour limit: %+v", u)
	}
	if !u.WeeklyKnown || u.Weekly != 30 || !u.WeeklyReset.Equal(policyNow.Add(48*time.Hour)) {
		t.Fatalf("weekly limit: %+v", u)
	}
	if !u.CheckedAt.Equal(policyNow) || u.SessionIdle {
		t.Fatalf("when it was read, and that the window is running: %+v", u)
	}
}

// A number the service did not really give is never shown as a measured one.
func TestUsageNeverTurnsMissingDataIntoANumber(t *testing.T) {
	session := func(u *Usage) bool { return u.SessionKnown }
	weekly := func(u *Usage) bool { return u.WeeklyKnown }
	tests := []struct {
		name   string
		change func(map[string]any)
		known  func(*Usage) bool
	}{
		{"missing session", func(b map[string]any) { delete(b, "five_hour") }, session},
		{"null session", func(b map[string]any) { b["five_hour"] = nil }, session},
		{"missing week", func(b map[string]any) { delete(b, "seven_day") }, weekly},
		{"null week", func(b map[string]any) { b["seven_day"] = nil }, weekly},
		{"missing utilization", func(b map[string]any) { delete(b["five_hour"].(map[string]any), "utilization") }, session},
		{"null utilization", func(b map[string]any) { b["five_hour"].(map[string]any)["utilization"] = nil }, session},
		{"string utilization", func(b map[string]any) { b["five_hour"].(map[string]any)["utilization"] = "0" }, session},
		{"negative utilization", func(b map[string]any) { b["five_hour"].(map[string]any)["utilization"] = -1 }, session},
		{"over 100 utilization", func(b map[string]any) { b["five_hour"].(map[string]any)["utilization"] = 101 }, session},
		{"missing reset", func(b map[string]any) { delete(b["seven_day"].(map[string]any), "resets_at") }, weekly},
		{"null reset", func(b map[string]any) { b["seven_day"].(map[string]any)["resets_at"] = nil }, weekly},
		{"invalid reset", func(b map[string]any) { b["seven_day"].(map[string]any)["resets_at"] = "tomorrow" }, weekly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := usageFixture()
			tt.change(body)
			if u := parseFixture(t, body); tt.known(u) {
				t.Fatalf("missing or invalid data was taken for a measured number: %+v", u)
			}
		})
	}
}

func TestUsageRejectsInvalidDocument(t *testing.T) {
	for _, body := range []string{"null", "[]", "broken", `{"five_hour":`} {
		if _, err := parseUsage([]byte(body), policyNow); err == nil {
			t.Fatalf("accepted invalid document %q", body)
		}
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUsageTransportIsBoundedAndSanitized(t *testing.T) {
	original := client
	t.Cleanup(func() { client = original })
	for _, tt := range []struct {
		name               string
		status             int
		body               string
		signedOut, limited bool
	}{
		{"unauthorized", 401, "private fixture response", true, false},
		{"rate limit", 429, "private fixture response", false, true},
		{"server error", 500, "private fixture response", false, false},
		{"invalid response", 200, "private fixture response", false, false},
		{"oversize", 200, strings.Repeat(" ", (1<<20)+1), false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client = &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			var out any
			err := apiGet(context.Background(), "fixture-only", "/api/oauth/usage", &out)
			if err == nil || strings.Contains(err.Error(), "private fixture response") {
				t.Fatalf("response not rejected safely: %v", err)
			}
			if tt.signedOut && !errors.Is(err, errSignedOut) {
				t.Fatal("401 did not invalidate authentication")
			}
			var limited *rateLimited
			if tt.limited && (!errors.As(err, &limited) || limited.after != time.Minute) {
				t.Fatal("rate-limit backoff not preserved")
			}
		})
	}
}

func TestServiceClientRejectsRedirects(t *testing.T) {
	if client.CheckRedirect == nil || client.CheckRedirect(nil, nil) == nil {
		t.Fatal("authenticated service redirects must be rejected")
	}
}
