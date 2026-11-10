package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opaque synthetic strings stand in for Claude's encrypted values. Nothing
// here reads, decrypts or submits a real account's sign-in.
func signInOnDisk(t *testing.T, d desktop) string {
	t.Helper()
	cfg, err := readObject(d.configPath())
	if err != nil {
		t.Fatal(err)
	}
	keys := "none"
	if raw, ok := cfg.get("oauth:tokenCacheV2"); ok {
		var value string
		_ = json.Unmarshal(raw, &value)
		keys = strings.TrimPrefix(value, "fixture-")
		var remembered string
		if raw, ok := cfg.get("lastKnownAccountUuid"); ok {
			_ = json.Unmarshal(raw, &remembered)
		}
		if remembered != keys {
			t.Fatalf("the remembered account %q and the token cache %q disagree", remembered, keys)
		}
	} else if _, ok := cfg.get("lastKnownAccountUuid"); ok {
		t.Fatal("the remembered account must be removed together with the token cache")
	}
	cookies := "none"
	if data, err := os.ReadFile(d.cookieFiles()[0]); err == nil {
		cookies = strings.TrimPrefix(string(data), "cookies-")
	}
	return "keys:" + keys + " cookies:" + cookies
}

func restoreFixture(t *testing.T) (desktop, string) {
	t.Helper()
	root := t.TempDir()
	d := desktop{dir: filepath.Join(root, "claude")}
	other := desktop{dir: filepath.Join(root, "claude-b")}
	write(t, other.configPath(), `{"lastKnownAccountUuid":"b","oauth:tokenCacheV2":"fixture-b","oauth:staleSessionLatch":"latch-b"}`)
	write(t, other.cookieFiles()[0], "cookies-b")
	saved := filepath.Join(root, "accounts", "b")
	if err := saveProfile(other, saved); err != nil {
		t.Fatal(err)
	}
	write(t, d.configPath(), `{"locale":"en-US","lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-a","oauth:staleSessionLatch":"latch-a","oauth:everydayRevokeOwed":"shared"}`)
	write(t, d.cookieFiles()[0], "cookies-a")
	return d, saved
}

// If Claude starts while its sign-in is half replaced and finds one account's
// tokens next to another account's web session, it treats that as an account
// takeover and revokes the tokens. The restore therefore goes: remove the
// sign-in settings, swap the cookies, write the new settings. At every point
// in between, what is on disk is one account or nobody, never a mix.
func TestRestoreNeverLeavesOneAccountsTokensWithAnothersCookies(t *testing.T) {
	d, saved := restoreFixture(t)
	var seen []string
	if err := restoreProfileSafely(d, saved, func() bool { seen = append(seen, signInOnDisk(t, d)); return true }); err != nil {
		t.Fatal(err)
	}
	want := []string{"keys:a cookies:a", "keys:none cookies:a", "keys:none cookies:b"}
	if strings.Join(seen, " | ") != strings.Join(want, " | ") {
		t.Fatalf("states before each write = %v, want %v", seen, want)
	}
	if got := signInOnDisk(t, d); got != "keys:b cookies:b" {
		t.Fatalf("after the restore: %s", got)
	}
	cfg, _ := readObject(d.configPath())
	if v, _ := cfg.get("oauth:staleSessionLatch"); string(v) != `"latch-b"` {
		t.Fatal("the account's own latch travels with its sign-in")
	}
	if v, _ := cfg.get("oauth:everydayRevokeOwed"); string(v) != `"shared"` {
		t.Fatal("settings that belong to the app, not the account, stay untouched")
	}
	if v, _ := cfg.get("locale"); string(v) != `"en-US"` {
		t.Fatal("unrelated settings stay untouched")
	}
}

func TestRestoreStopsCleanlyWhenClaudeReappears(t *testing.T) {
	for stop, want := range []string{"keys:a cookies:a", "keys:none cookies:a", "keys:none cookies:b"} {
		d, saved := restoreFixture(t)
		calls := 0
		err := restoreProfileSafely(d, saved, func() bool { calls++; return calls != stop+1 })
		if errorCode(err) != "CLAUDE_REOPENED" {
			t.Fatalf("stop %d: want CLAUDE_REOPENED, got %v", stop, err)
		}
		if got := signInOnDisk(t, d); got != want {
			t.Fatalf("stop %d: left %q on disk, want %q", stop, got, want)
		}
	}
}

func TestRestoreRefusesADamagedSavedSignInBeforeTouchingAnything(t *testing.T) {
	d, saved := restoreFixture(t)
	write(t, filepath.Join(saved, "Cookies"), "tampered")
	if err := restoreProfileSafely(d, saved, func() bool { return true }); err == nil {
		t.Fatal("a saved sign-in that fails its seal must be refused")
	}
	if got := signInOnDisk(t, d); got != "keys:a cookies:a" {
		t.Fatalf("nothing may change: %s", got)
	}
}

// A save replaces the stored sign-in for that account. The copy it replaces
// is kept as one previous generation, so a bad save cannot destroy the last
// good one.
func TestSavingASignInKeepsThePreviousCopy(t *testing.T) {
	root := t.TempDir()
	d := desktop{dir: filepath.Join(root, "claude")}
	dir := filepath.Join(root, "accounts", "a")
	previous := dir + ".previous"
	write(t, d.configPath(), `{"lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-first"}`)
	write(t, d.cookieFiles()[0], "cookies-first")
	if err := saveProfileKeepingPrevious(d, dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatal("the first save has nothing to keep")
	}
	write(t, d.configPath(), `{"lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-second"}`)
	write(t, d.cookieFiles()[0], "cookies-second")
	if err := saveProfileKeepingPrevious(d, dir, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "config.json")), "fixture-second") || read(t, filepath.Join(dir, "Cookies")) != "cookies-second" {
		t.Fatal("the saved sign-in must be the newest one")
	}
	if !strings.Contains(read(t, filepath.Join(previous, "config.json")), "fixture-first") || read(t, filepath.Join(previous, "Cookies")) != "cookies-first" {
		t.Fatal("the replaced sign-in must be kept as the previous copy")
	}
	if err := validateSavedProfile(dir); err != nil {
		t.Fatalf("the new copy is complete: %v", err)
	}
	if err := validateSavedProfile(previous); err != nil {
		t.Fatalf("the previous copy is complete: %v", err)
	}
	for _, path := range []string{dir, previous} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("saved sign-ins stay private: %s", filepath.Base(path))
		}
	}

	// A save that cannot even read Claude's settings leaves both copies alone.
	write(t, d.configPath(), `not json`)
	if err := saveProfileKeepingPrevious(d, dir, nil); err == nil {
		t.Fatal("an unreadable settings file must fail the save")
	}
	if !strings.Contains(read(t, filepath.Join(dir, "config.json")), "fixture-second") || !strings.Contains(read(t, filepath.Join(previous, "config.json")), "fixture-first") {
		t.Fatal("a failed save must not disturb the stored copies")
	}
}

// The new copy is checked before it replaces anything. A copy that fails the
// check is thrown away and both stored copies stay exactly as they were.
func TestSavingASignInChecksTheNewCopyBeforeItReplacesTheOldOne(t *testing.T) {
	root := t.TempDir()
	d := desktop{dir: filepath.Join(root, "claude")}
	dir := filepath.Join(root, "accounts", "a")
	write(t, d.configPath(), `{"lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-first"}`)
	write(t, d.cookieFiles()[0], "cookies-first")
	if err := saveProfileKeepingPrevious(d, dir, nil); err != nil {
		t.Fatal(err)
	}
	write(t, d.configPath(), `{"lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-second"}`)
	write(t, d.cookieFiles()[0], "cookies-second")
	if err := saveProfileKeepingPrevious(d, dir, nil); err != nil {
		t.Fatal(err)
	}
	// Now Claude's files hold something that must not be kept.
	write(t, d.configPath(), `{"lastKnownAccountUuid":"someone-else","oauth:tokenCacheV2":"fixture-wrong"}`)
	write(t, d.cookieFiles()[0], "cookies-wrong")
	var checked string
	err := saveProfileKeepingPrevious(d, dir, func(staging string) error {
		checked = read(t, filepath.Join(staging, "config.json"))
		return coded("SAVED_SIGNIN_MISMATCH", "fixture: this copy belongs to somebody else")
	})
	if errorCode(err) != "SAVED_SIGNIN_MISMATCH" || !strings.Contains(checked, "fixture-wrong") {
		t.Fatalf("the check sees the new copy and its refusal fails the save: %v", err)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "config.json")), "fixture-second") || read(t, filepath.Join(dir, "Cookies")) != "cookies-second" {
		t.Fatal("the stored copy is untouched")
	}
	if !strings.Contains(read(t, filepath.Join(dir+".previous", "config.json")), "fixture-first") {
		t.Fatal("the previous copy is untouched")
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 2 {
		t.Fatalf("the rejected copy is removed: %d entries", len(entries))
	}
}
