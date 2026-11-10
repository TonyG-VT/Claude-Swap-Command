package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// Test sign-in caches are plain JSON in base64; plainCache stands in for the
// real decryption, which needs the Mac's keychain.
func plainCache(data []byte) ([]byte, error) { return data, nil }

type fakeToken struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

func fakeCacheKey(account, org string) string {
	return "acct:" + account + "|client-id:" + org + ":https://api.example.invalid:user:profile user:inference"
}

// fakeSignInCache builds an oauth:tokenCacheV2 value. A nil entry is how
// Claude leaves a slot behind after it has logged an account out.
func fakeSignInCache(t *testing.T, entries map[string]*fakeToken) string {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func writeSignInConfig(t *testing.T, path, lastKnown, cache string) {
	t.Helper()
	cfg := map[string]any{"locale": "en-US"}
	if lastKnown != "" {
		cfg["lastKnownAccountUuid"] = lastKnown
	}
	if cache != "" {
		cfg["oauth:tokenCacheV2"] = cache
	}
	data, _ := json.Marshal(cfg)
	write(t, path, string(data))
}

func TestSignedInNeedsALiveEntryNotJustTheKey(t *testing.T) {
	a, orgA, b, orgB := carryID(0xA1), carryID(0xA2), carryID(0xB1), carryID(0xB2)
	path := filepath.Join(t.TempDir(), "config.json")
	live := &fakeToken{Token: "fixture-token-one", RefreshToken: "fixture-refresh", ExpiresAt: 1}

	writeSignInConfig(t, path, a, fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): live}))
	state, err := readSignIn(path, plainCache)
	if err != nil || state.Account != a || state.Org != orgA || state.SignedOut() || state.Multiple() {
		t.Fatalf("a live entry means signed in as that account: %+v %v", state, err)
	}
	first := state.Fingerprint

	// An expired token is still a sign-in: Claude renews it from the session.
	live.Token = "fixture-token-two"
	writeSignInConfig(t, path, a, fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): live}))
	state, _ = readSignIn(path, plainCache)
	if state.Account != a || state.Fingerprint == first || state.Fingerprint == "" {
		t.Fatalf("the fingerprint must change when Claude renews a token: %+v", state)
	}

	// Claude keeps the key and lastKnownAccountUuid when it logs out and only
	// empties the entries. That is signed out.
	for name, cache := range map[string]string{
		"emptied":   fakeSignInCache(t, map[string]*fakeToken{}),
		"null slot": fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): nil}),
		"no token":  fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): {}}),
		"no key":    "",
	} {
		writeSignInConfig(t, path, a, cache)
		state, err := readSignIn(path, plainCache)
		if err != nil || !state.SignedOut() || state.Account != "" {
			t.Fatalf("%s: must read as signed out even though the last account is still named: %+v %v", name, state, err)
		}
	}

	// Two accounts at once is Claude's own account switcher, not ours.
	writeSignInConfig(t, path, a, fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): live, fakeCacheKey(b, orgB): live}))
	state, err = readSignIn(path, plainCache)
	if err != nil || !state.Multiple() || state.Account != "" || len(state.Accounts) != 2 {
		t.Fatalf("two accounts with live entries: %+v %v", state, err)
	}

	// One account in two organisations is still one account; the org is then unknown.
	writeSignInConfig(t, path, a, fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): live, fakeCacheKey(a, orgB): live}))
	state, err = readSignIn(path, plainCache)
	if err != nil || state.Account != a || state.Org != "" {
		t.Fatalf("one account, two organisations: %+v %v", state, err)
	}
}

func TestSignInReadFailuresAreNamed(t *testing.T) {
	a, orgA := carryID(0xA1), carryID(0xA2)
	dir := t.TempDir()
	if _, err := readSignIn(filepath.Join(dir, "missing.json"), plainCache); errorCode(err) != "SIGNIN_STORE_UNREADABLE" {
		t.Fatalf("a missing settings file: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	writeSignInConfig(t, path, a, "not base64 !!!")
	if _, err := readSignIn(path, plainCache); errorCode(err) != "SIGNIN_FORMAT_CHANGED" {
		t.Fatalf("an undecodable cache: %v", err)
	}
	writeSignInConfig(t, path, a, base64.StdEncoding.EncodeToString([]byte(`["not","an","object"]`)))
	if _, err := readSignIn(path, plainCache); errorCode(err) != "SIGNIN_FORMAT_CHANGED" {
		t.Fatalf("a cache that is not an object: %v", err)
	}
	writeSignInConfig(t, path, a, fakeSignInCache(t, map[string]*fakeToken{fakeCacheKey(a, orgA): {Token: "fixture-token"}}))
	locked := func([]byte) ([]byte, error) { return nil, errKeychainUnavailable }
	if _, err := readSignIn(path, locked); errorCode(err) != "KEYCHAIN_UNAVAILABLE" {
		t.Fatalf("a locked keychain is its own cause, not a format change: %v", err)
	}
	broken := func([]byte) ([]byte, error) { return nil, errors.New("fixture decrypt failure") }
	if _, err := readSignIn(path, broken); errorCode(err) != "SIGNIN_FORMAT_CHANGED" {
		t.Fatalf("a cache that will not decrypt: %v", err)
	}
}

// saveFixtureProfile writes a saved sign-in the way saveProfile does, from a
// pretend Claude data folder.
func saveFixtureProfile(t *testing.T, dir, lastKnown string, entries map[string]*fakeToken) {
	t.Helper()
	d := desktop{dir: filepath.Join(t.TempDir(), "claude")}
	writeSignInConfig(t, d.configPath(), lastKnown, fakeSignInCache(t, entries))
	write(t, d.cookieFiles()[0], "SQLite format 3\x00fixture-cookies")
	if err := saveProfile(d, dir); err != nil {
		t.Fatal(err)
	}
}

// Before a switch the saved sign-in is checked without the network: it must
// be complete, and it must belong to the account whose folder it sits in.
func TestSavedSignInMustBelongToItsAccount(t *testing.T) {
	a, orgA, b, orgB := carryID(0xA1), carryID(0xA2), carryID(0xB1), carryID(0xB2)
	root := t.TempDir()
	dir := filepath.Join(root, "accounts", a)
	token := &fakeToken{Token: "fixture-token", ExpiresAt: 1}

	saveFixtureProfile(t, dir, a, map[string]*fakeToken{fakeCacheKey(a, orgA): token})
	saved, err := checkSavedSignIn(dir, a, plainCache)
	if err != nil || saved.Empty || saved.SavedAt.IsZero() {
		t.Fatalf("a complete saved sign-in for its own account passes: %+v %v", saved, err)
	}

	// The entries, or the remembered account, name somebody else.
	for name, save := range map[string]func(){
		"entries for another account": func() { saveFixtureProfile(t, dir, a, map[string]*fakeToken{fakeCacheKey(b, orgB): token}) },
		"remembered account differs":  func() { saveFixtureProfile(t, dir, b, map[string]*fakeToken{fakeCacheKey(a, orgA): token}) },
		"two accounts inside": func() {
			saveFixtureProfile(t, dir, a, map[string]*fakeToken{fakeCacheKey(a, orgA): token, fakeCacheKey(b, orgB): token})
		},
	} {
		save()
		if _, err := checkSavedSignIn(dir, a, plainCache); errorCode(err) != "SAVED_SIGNIN_MISMATCH" {
			t.Fatalf("%s: want SAVED_SIGNIN_MISMATCH, got %v", name, err)
		}
	}

	// Saved while logged out: usable only if the web session is still good, so
	// it is flagged, not refused.
	saveFixtureProfile(t, dir, a, map[string]*fakeToken{})
	if saved, err := checkSavedSignIn(dir, a, plainCache); err != nil || !saved.Empty {
		t.Fatalf("a saved sign-in without a live entry is flagged, not refused: %+v %v", saved, err)
	}

	// A damaged copy and a missing one have their own causes.
	saveFixtureProfile(t, dir, a, map[string]*fakeToken{fakeCacheKey(a, orgA): token})
	write(t, filepath.Join(dir, "Cookies"), "tampered")
	if _, err := checkSavedSignIn(dir, a, plainCache); errorCode(err) != "SAVED_SIGNIN_DAMAGED" {
		t.Fatalf("a saved sign-in that fails its seal: %v", err)
	}
	if _, err := checkSavedSignIn(filepath.Join(root, "accounts", b), b, plainCache); errorCode(err) != "NO_SAVED_SIGNIN" {
		t.Fatalf("no saved sign-in at all: %v", err)
	}
}
