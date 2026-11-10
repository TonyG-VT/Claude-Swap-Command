// Modified for Claude Swap Command (2026).
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const sampleConfig = "{\n\t\"locale\": \"en-US\",\n\t\"oauth:tokenCache\": \"old\",\n\t\"nested\": {\n\t\t\"a\": [\n\t\t\t1,\n\t\t\t2\n\t\t]\n\t},\n\t\"lastKnownAccountUuid\": \"acc-a\",\n\t\"oauth:tokenCacheV2\": \"djEwAAAA\",\n\t\"zoom\": 1.5\n}"

// accountInSettings is the account Claude's settings file names, or "" when
// its sign-in entries are gone.
func accountInSettings(t *testing.T, d desktop) string {
	t.Helper()
	cfg, err := readObject(d.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.get("oauth:tokenCacheV2"); !ok {
		return ""
	}
	var id string
	if raw, ok := cfg.get("lastKnownAccountUuid"); ok {
		_ = json.Unmarshal(raw, &id)
	}
	return id
}

func TestObjectRoundTripKeepsFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, sampleConfig)
	o, err := readObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.write(path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != sampleConfig {
		t.Fatalf("round trip changed the file:\n%s", got)
	}
}

// The real app config must survive a round trip byte for byte.
// Set CLAUDE_CONFIG_SAMPLE to a copy of config.json to run it.
func TestRealConfigRoundTrip(t *testing.T) {
	src := os.Getenv("CLAUDE_CONFIG_SAMPLE")
	if src == "" {
		t.Skip("CLAUDE_CONFIG_SAMPLE not set")
	}
	want := read(t, src)
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, want)
	o, err := readObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.write(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != want {
		t.Fatal("real config changed on round trip")
	}
}

func TestProfileSwapKeepsSharedSettings(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	profiles := t.TempDir()
	write(t, d.configPath(), sampleConfig)
	write(t, d.cookieFiles()[0], "cookies-a")

	if err := saveProfile(d, filepath.Join(profiles, "a")); err != nil {
		t.Fatal(err)
	}
	if err := clearSignIn(d); err != nil {
		t.Fatal(err)
	}
	if id := accountInSettings(t, d); id != "" {
		t.Fatalf("still signed in as %q", id)
	}
	if _, err := os.Stat(d.cookieFiles()[0]); !os.IsNotExist(err) {
		t.Fatal("cookies not cleared")
	}

	// Account b signs in and changes a shared setting.
	cfg, _ := readObject(d.configPath())
	cfg.set("lastKnownAccountUuid", json.RawMessage(`"acc-b"`))
	cfg.set("oauth:tokenCacheV2", json.RawMessage(`"djEwBBBB"`))
	cfg.set("zoom", json.RawMessage(`2`))
	if err := cfg.write(d.configPath()); err != nil {
		t.Fatal(err)
	}
	write(t, d.cookieFiles()[0], "cookies-b")
	write(t, d.cookieFiles()[1], "journal-b")
	if err := saveProfile(d, filepath.Join(profiles, "b")); err != nil {
		t.Fatal(err)
	}

	if err := restoreProfileSafely(d, filepath.Join(profiles, "a"), func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if id := accountInSettings(t, d); id != "acc-a" {
		t.Fatalf("active = %q, want acc-a", id)
	}
	if got := read(t, d.cookieFiles()[0]); got != "cookies-a" {
		t.Fatalf("cookies = %q", got)
	}
	if _, err := os.Stat(d.cookieFiles()[1]); !os.IsNotExist(err) {
		t.Fatal("journal of b left behind")
	}
	cfg, _ = readObject(d.configPath())
	if v, _ := cfg.get("zoom"); string(v) != "2" {
		t.Fatalf("shared setting lost: zoom = %s", v)
	}
	if v, _ := cfg.get("oauth:tokenCache"); string(v) != `"old"` {
		t.Fatalf("tokenCache = %s", v)
	}
}

func TestUsageTextShowsWeeklyReset(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	a := Account{Usage: &Usage{Session: 20, Weekly: 40, SessionKnown: true, WeeklyKnown: true, CheckedAt: now, SessionReset: now.Add(time.Hour), WeeklyReset: now.Add(2 * 24 * time.Hour)}}
	if got, want := usageText(a, 96, now), "5h 20% · week 40%, resets Thu 12:00"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	a.Usage.Weekly = 97
	if got, want := usageText(a, 96, now), "5h 20% · week 97% until Thu 12:00"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	a.Usage.Weekly, a.Usage.WeeklyReset = 0, now.Add(6*24*time.Hour+time.Hour)
	if got, want := usageText(a, 96, now), "5h 20% · week 0%, resets Mon 12 Oct 13:00"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCookieFilesFollowAppLayout(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	if got, want := d.cookieFiles()[0], filepath.Join(d.dir, "Cookies"); got != want {
		t.Fatalf("without Network/: %q, want %q", got, want)
	}
	if err := os.Mkdir(filepath.Join(d.dir, "Network"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := d.cookieFiles()[0], filepath.Join(d.dir, "Network", "Cookies"); got != want {
		t.Fatalf("with Network/: %q, want %q", got, want)
	}
}
