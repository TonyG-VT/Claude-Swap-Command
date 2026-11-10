package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUsageResetAcceptsReviewedEpochSeconds(t *testing.T) {
	body := usageFixture()
	body["five_hour"].(map[string]any)["resets_at"] = policyNow.Add(2 * time.Hour).Unix()
	u := parseFixture(t, body)
	if !u.SessionKnown || !u.SessionReset.Equal(policyNow.Add(2*time.Hour)) {
		t.Fatal("native epoch-second reset was not recognized")
	}
}

func TestUsageResetRejectsInvalidNumericValues(t *testing.T) {
	for _, value := range []string{"null", "0", "-1", "1e30", "1793457600000", "true", "{}", "\"1793457600\""} {
		raw := json.RawMessage(`{"utilization":0,"resets_at":` + value + `}`)
		if parseUsageWindow(raw).Known {
			t.Fatalf("accepted invalid or ambiguous reset %s", value)
		}
	}
}

func TestUsageTextDoesNotDisplayUnknownAsZero(t *testing.T) {
	u := parseFixture(t, usageFixture())
	u.Session, u.SessionKnown, u.SessionReset = 0, false, time.Time{}
	got := usageText(Account{Usage: u}, 96, policyNow)
	if !strings.Contains(got, "5h unknown") || strings.Contains(got, "5h 0%") || !strings.Contains(got, "week 30%") {
		t.Fatalf("unknown session allowance misrepresented: %q", got)
	}
	u.WeeklyKnown = false
	got = usageText(Account{Usage: u}, 96, policyNow)
	if got != "5h unknown · week unknown" {
		t.Fatalf("unknown weekly allowance misrepresented: %q", got)
	}
}

func TestUsageTextLabelsElapsedResetWithoutInventingCapacity(t *testing.T) {
	u := parseFixture(t, usageFixture())
	u.Session, u.SessionReset = 100, policyNow.Add(-time.Second)
	got := usageText(Account{Usage: u}, 96, policyNow)
	if !strings.Contains(got, "5h 100% (refresh needed)") {
		t.Fatalf("elapsed reset not identified: %q", got)
	}
}
