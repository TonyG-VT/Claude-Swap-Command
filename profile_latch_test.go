package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Opaque synthetic strings stand in for provider-encrypted bytes. These tests
// never read, decrypt, refresh or submit a real account's credentials.
func TestProfilePreservesScopedStaleLatchAndSharedRevocationDebt(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	profiles := t.TempDir()
	write(t, d.configPath(), `{"lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-a","oauth:staleSessionLatch":"opaque-latch-a","oauth:everydayRevokeOwed":"shared-owed-a","oauth:everydayRevokeOwedUnreadable":"shared-unreadable-a"}`)
	if err := saveProfile(d, filepath.Join(profiles, "a")); err != nil {
		t.Fatal(err)
	}
	saved, err := readObject(filepath.Join(profiles, "a", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := saved.get("oauth:staleSessionLatch"); !ok || string(value) != `"opaque-latch-a"` {
		t.Fatal("account re-login evidence was not saved")
	}
	for _, key := range []string{"oauth:everydayRevokeOwed", "oauth:everydayRevokeOwedUnreadable"} {
		if _, ok := saved.get(key); ok {
			t.Fatal("shared revocation debt was copied into an account profile")
		}
	}
	if err := clearSignIn(d); err != nil {
		t.Fatal(err)
	}
	cfg, err := readObject(d.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.get("oauth:staleSessionLatch"); ok {
		t.Fatal("old account's latch leaked into new sign-in")
	}
	for key, want := range map[string]string{"oauth:everydayRevokeOwed": `"shared-owed-a"`, "oauth:everydayRevokeOwedUnreadable": `"shared-unreadable-a"`} {
		if value, _ := cfg.get(key); string(value) != want {
			t.Fatal("clearing sign-in lost shared revocation debt")
		}
	}
	cfg.set("lastKnownAccountUuid", json.RawMessage(`"b"`))
	cfg.set("oauth:tokenCacheV2", json.RawMessage(`"fixture-b"`))
	cfg.set("oauth:staleSessionLatch", json.RawMessage(`"opaque-latch-b"`))
	cfg.set("oauth:everydayRevokeOwed", json.RawMessage(`"newer-shared-owed"`))
	if err := cfg.write(d.configPath()); err != nil {
		t.Fatal(err)
	}
	if err := saveProfile(d, filepath.Join(profiles, "b")); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"a", "b", "a"} {
		if err := restoreProfileSafely(d, filepath.Join(profiles, account), func() bool { return true }); err != nil {
			t.Fatal(err)
		}
		cfg, err = readObject(d.configPath())
		if err != nil {
			t.Fatal(err)
		}
		if value, _ := cfg.get("oauth:staleSessionLatch"); string(value) != `"opaque-latch-`+account+`"` {
			t.Fatal("wrong account's re-login evidence restored")
		}
		if value, _ := cfg.get("oauth:everydayRevokeOwed"); string(value) != `"newer-shared-owed"` {
			t.Fatal("restoring profile overwrote newer revocation debt")
		}
		if value, _ := cfg.get("oauth:everydayRevokeOwedUnreadable"); string(value) != `"shared-unreadable-a"` {
			t.Fatal("restoring profile removed unreadable revocation debt")
		}
	}
}

func TestLegacyProfileWithoutLatchRemainsReadable(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	profile := filepath.Join(t.TempDir(), "legacy")
	write(t, filepath.Join(profile, "config.json"), `{"lastKnownAccountUuid":"old","oauth:tokenCacheV2":"fixture-old"}`)
	write(t, d.configPath(), `{"lastKnownAccountUuid":"new","oauth:tokenCacheV2":"fixture-new","oauth:staleSessionLatch":"other-account-latch","oauth:everydayRevokeOwed":"shared-debt"}`)
	if err := restoreProfileSafely(d, profile, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	cfg, err := readObject(d.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.get("oauth:staleSessionLatch"); ok {
		t.Fatal("absent legacy latch inherited another account's evidence")
	}
	if value, _ := cfg.get("oauth:everydayRevokeOwed"); string(value) != `"shared-debt"` {
		t.Fatal("legacy restore erased shared revocation debt")
	}
	if id := accountInSettings(t, d); id != "old" {
		t.Fatal("legacy profile identity not restored")
	}
}

func TestProfileRollbackRefusesNewerGlobalRevocationDebt(t *testing.T) {
	d, root := transactionFixture(t)
	write(t, d.configPath(), `{"lastKnownAccountUuid":"a","oauth:tokenCacheV2":"fixture-a","oauth:staleSessionLatch":"opaque-latch-a","oauth:everydayRevokeOwed":"debt-before"}`)
	write(t, d.cookieFiles()[0], "synthetic-cookie-a")
	bk := mustBackup(t, d, root)
	mustWriteProtected(t, d.configPath(), `{"lastKnownAccountUuid":"b","oauth:tokenCacheV2":"fixture-b","oauth:staleSessionLatch":"opaque-latch-b","oauth:everydayRevokeOwed":"debt-before"}`)
	mustWriteProtected(t, d.cookieFiles()[0], "synthetic-cookie-b")
	if err := sealBackup(d, bk); err != nil {
		t.Fatal(err)
	}
	newer := `{"lastKnownAccountUuid":"b","oauth:tokenCacheV2":"fixture-b","oauth:staleSessionLatch":"opaque-latch-b","oauth:everydayRevokeOwed":"newer-debt","oauth:everydayRevokeOwedUnreadable":"newer-unreadable-debt"}`
	write(t, d.configPath(), newer)
	if err := restoreBackup(d, bk); err == nil {
		t.Fatal("rollback erased newer provider revocation debt")
	}
	if read(t, d.configPath()) != newer || read(t, d.cookieFiles()[0]) != "synthetic-cookie-b" {
		t.Fatal("rollback mutated files before rejecting newer revocation debt")
	}
}
