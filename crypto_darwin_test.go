package main

import (
	"errors"
	"testing"
)

// A keychain that was locked or denied once must be asked again next time,
// not remembered as a failure until the helper restarts.
func TestAppKeyIsAskedAgainAfterAFailure(t *testing.T) {
	previousRead, previousKey := keychainPassword, keyVal
	defer func() { keychainPassword, keyVal = previousRead, previousKey }()
	keyVal = nil
	calls := 0
	keychainPassword = func() (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("fixture: keychain locked")
		}
		return "fixture-password", nil
	}
	if _, err := appKey(); !errors.Is(err, errKeychainUnavailable) {
		t.Fatalf("a keychain failure has its own cause: %v", err)
	}
	key, err := appKey()
	if err != nil || len(key) != 16 {
		t.Fatalf("the next call must ask the keychain again: %v", err)
	}
	if _, err := appKey(); err != nil || calls != 2 {
		t.Fatalf("a key that was read is kept: calls=%d err=%v", calls, err)
	}
}
