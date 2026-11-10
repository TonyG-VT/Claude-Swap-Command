package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func profileUpgradeFixture() (Account, profile) {
	a := Account{UUID: "fixture-account", Org: "fixture-org", Name: "Old display name", Email: "old@example.invalid", Plan: "Pro"}
	var p profile
	p.Account.UUID, p.Organization.UUID = a.UUID, a.Org
	p.Account.DisplayName, p.Account.Email = "Updated display name", "updated@example.invalid"
	p.Organization.Type = "claude_max"
	// An old flag on the account must not override the organisation's current plan.
	p.Account.HasPro = true
	return a, p
}

func TestProfileRefreshUpdatesNameEmailAndPlanTogether(t *testing.T) {
	a, p := profileUpgradeFixture()
	if err := a.refreshProfile(p); err != nil {
		t.Fatal(err)
	}
	if got := accountTitle(1, a); got != "Account 1: Updated display name (updated@example.invalid, Max)" {
		t.Fatalf("an upgraded account still shows stale details: %q", got)
	}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var restored Account
	if err := json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(restored, a) {
		t.Fatalf("the refreshed details are what gets saved: %v", err)
	}
}

func TestProfileRefreshRejectsWrongIdentityWithoutChangingAccount(t *testing.T) {
	for name, change := range map[string]func(*profile){
		"wrong account":        func(p *profile) { p.Account.UUID = "another-account" },
		"wrong organization":   func(p *profile) { p.Organization.UUID = "another-org" },
		"missing organization": func(p *profile) { p.Organization.UUID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			a, p := profileUpgradeFixture()
			before := a
			change(&p)
			if err := a.refreshProfile(p); err == nil {
				t.Fatal("a profile for somebody else was accepted")
			}
			if !reflect.DeepEqual(before, a) {
				t.Fatal("a rejected profile changed the saved account")
			}
		})
	}
}
