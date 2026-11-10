// Modified for Claude Swap Command (2026).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A profile is a saved sign-in: the account's config.json entries and the
// app's cookie store. Tokens stay encrypted by the app's own key, the
// switcher never writes them in plain text.

// saveProfile copies the current sign-in into dir. The app must be closed.
func saveProfile(d desktop, dir string) error {
	cfg, err := readObject(d.configPath())
	if err != nil {
		return err
	}
	saved := &object{vals: map[string]json.RawMessage{}}
	for _, k := range authKeys {
		if v, ok := cfg.get(k); ok {
			saved.set(k, v)
		}
	}
	if err := ensurePrivateDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(dir); err != nil {
		return err
	}
	// A partial multi-file save is never treated as a usable saved login.
	// The app's original encrypted bytes remain encrypted; the seal is hashes only.
	if err := writeFileAtomic(filepath.Join(dir, ".save-pending"), []byte("profile save incomplete")); err != nil {
		return err
	}
	if err := saved.write(filepath.Join(dir, "config.json")); err != nil {
		return err
	}
	for _, f := range d.cookieFiles() {
		if err := copyFile(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			return err
		}
	}
	files := append([]string{"config.json"}, profileCookieNames...)
	seal := make(map[string]fileFingerprint, len(files))
	for _, name := range files {
		state, err := fingerprintFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		seal[name] = state
	}
	data, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "profile-manifest.json"), data); err != nil {
		return err
	}
	return removeProtectedFile(filepath.Join(dir, ".save-pending"))
}

var profileCookieNames = []string{"Cookies", "Cookies-journal", "Cookies-wal", "Cookies-shm"}

func validateSavedProfile(dir string) error {
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("saved profile directory is linked or invalid")
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, ".save-pending")); err == nil {
		return errors.New("saved profile is incomplete; sign in again before using it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, _, err := readStableFile(filepath.Join(dir, "profile-manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		// Existing upstream profiles can be read and gain a seal on next save.
		return nil
	}
	if err != nil {
		return err
	}
	var seal map[string]fileFingerprint
	if err := json.Unmarshal(data, &seal); err != nil {
		return errors.New("invalid saved-profile manifest")
	}
	for _, name := range append([]string{"config.json"}, profileCookieNames...) {
		want, ok := seal[name]
		if !ok {
			return errors.New("incomplete saved-profile manifest")
		}
		got, err := fingerprintFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if want.Exists != got.Exists || want.SHA256 != got.SHA256 || want.Size != got.Size {
			return errors.New("saved-profile integrity check failed")
		}
	}
	return nil
}

// clearSignIn removes the current sign-in locally so the app shows its login
// screen. It does not sign out, which would revoke the saved tokens.
func clearSignIn(d desktop) error {
	cfg, err := readObject(d.configPath())
	if err != nil {
		return err
	}
	for _, k := range authKeys {
		cfg.del(k)
	}
	for _, f := range d.cookieFiles() {
		if err := removeProtectedFile(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return cfg.write(d.configPath())
}

// restoreProfileSafely puts a saved sign-in in place in an order that stays
// safe if Claude starts in the middle of it. A Claude that finds one account's
// tokens next to another account's web session treats that as an account
// takeover and revokes the tokens. So the sign-in settings are removed first,
// then the cookies are swapped, then the new settings are written: at every
// point in between, what is on disk is one account or nobody, never a mix.
// closed is asked before each write; when it says no, nothing more is written.
func restoreProfileSafely(d desktop, dir string, closed func() bool) error {
	if err := validateSavedProfile(dir); err != nil {
		return err
	}
	saved, err := readObject(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	// Read every source before touching the destination; an unreadable saved
	// cookie file must not leave a partially restored account.
	type cookieCopy struct {
		path  string
		data  []byte
		state fileFingerprint
	}
	var cookies []cookieCopy
	for _, f := range d.cookieFiles() {
		data, state, err := readStableFile(filepath.Join(dir, filepath.Base(f)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("saved profile could not be read: %w", err)
		}
		cookies = append(cookies, cookieCopy{f, data, state})
	}
	reopened := coded("CLAUDE_REOPENED", "Claude reopened while the sign-in was being changed")

	if !closed() {
		return reopened
	}
	cfg, err := readObject(d.configPath())
	if err != nil {
		return err
	}
	for _, k := range authKeys {
		cfg.del(k)
	}
	if err := cfg.write(d.configPath()); err != nil {
		return err
	}

	if !closed() {
		return reopened
	}
	for _, cookie := range cookies {
		var err error
		if cookie.state.Exists {
			err = writeProtectedFile(cookie.path, cookie.data, &cookie.state)
		} else {
			err = removeProtectedFile(cookie.path)
		}
		if err != nil {
			return err
		}
	}

	if !closed() {
		return reopened
	}
	cfg, err = readObject(d.configPath())
	if err != nil {
		return err
	}
	for _, k := range authKeys {
		if v, ok := saved.get(k); ok {
			cfg.set(k, v)
		} else {
			cfg.del(k)
		}
	}
	return cfg.write(d.configPath())
}

// saveProfileKeepingPrevious saves the current sign-in into dir and keeps the
// copy it replaces as dir+".previous", one generation deep. The new copy is
// written next to dir first and handed to verify; only a copy that passes
// replaces anything, so a bad save never costs the last good one.
func saveProfileKeepingPrevious(d desktop, dir string, verify func(staging string) error) error {
	// A save that cannot even read Claude's settings must not disturb what is stored.
	if _, err := readObject(d.configPath()); err != nil {
		return err
	}
	staging := dir + ".new"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := saveProfile(d, staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if verify != nil {
		if err := verify(staging); err != nil {
			_ = os.RemoveAll(staging)
			return err
		}
	}
	previous := dir + ".previous"
	if _, err := os.Lstat(dir); err == nil {
		if validateSavedProfile(dir) == nil {
			if err := os.RemoveAll(previous); err != nil {
				_ = os.RemoveAll(staging)
				return err
			}
			if err := os.Rename(dir, previous); err != nil {
				_ = os.RemoveAll(staging)
				return err
			}
		} else if err := os.RemoveAll(dir); err != nil {
			// What was stored was already unusable; it is not worth keeping.
			_ = os.RemoveAll(staging)
			return err
		}
	}
	return os.Rename(staging, dir)
}
