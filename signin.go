package main

// Who is signed in, read from the sign-in cache itself.
//
// Claude keeps the oauth:tokenCacheV2 setting and lastKnownAccountUuid when it
// logs an account out; it only empties the entries inside the cache. So the
// presence of the setting says nothing. An account counts as signed in only
// while the decrypted cache holds a live entry for it. Entry keys look like
// "acct:<account>|<client>:<org>:<audience>:<scopes>".
//
// Nothing read here is logged or stored: callers get account and organisation
// identifiers and a fingerprint, never a token.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cacheDecrypter turns the stored cache value into its JSON. Production uses
// decryptValue (Claude's own key from the keychain).
type cacheDecrypter func([]byte) ([]byte, error)

var errKeychainUnavailable = coded("KEYCHAIN_UNAVAILABLE", "macOS did not let the helper read Claude's storage key; look for a macOS prompt asking for permission")

type signInState struct {
	Account     string   // the one account with a live entry; "" when signed out or when there are several
	Accounts    []string // every account with a live entry, sorted
	Org         string   // that account's organisation when all its entries agree
	Fingerprint string   // changes whenever Claude renews, replaces or drops an entry
	Entries     int      // how many live entries there are
}

func (s signInState) SignedOut() bool { return len(s.Accounts) == 0 }
func (s signInState) Multiple() bool  { return len(s.Accounts) > 1 }

func readSignIn(configPath string, decrypt cacheDecrypter) (signInState, error) {
	var state signInState
	cfg, err := readObject(configPath)
	if err != nil {
		return state, codedf("SIGNIN_STORE_UNREADABLE", "Claude's settings file could not be read (%w)", err)
	}
	raw, present := cfg.get("oauth:tokenCacheV2")
	if !present {
		return state, nil
	}
	changed := coded("SIGNIN_FORMAT_CHANGED", "Claude's saved sign-in could not be decoded; Claude may have changed how it stores sign-ins")
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return state, changed
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return state, changed
	}
	plain, err := decrypt(sealed)
	if errors.Is(err, errKeychainUnavailable) {
		return state, errKeychainUnavailable
	}
	if err != nil {
		return state, changed
	}
	var cache map[string]*struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if json.Unmarshal(plain, &cache) != nil || cache == nil {
		return state, changed
	}
	orgs := map[string]map[string]bool{}
	var live []string
	for key, entry := range cache {
		// An expired token still counts: Claude renews it from the web session.
		if entry == nil || entry.Token == "" || !strings.HasPrefix(key, "acct:") {
			continue
		}
		account, rest, found := strings.Cut(strings.TrimPrefix(key, "acct:"), "|")
		if !found || !validID(account) {
			continue
		}
		if orgs[account] == nil {
			orgs[account] = map[string]bool{}
		}
		if parts := strings.SplitN(rest, ":", 3); len(parts) >= 2 && validID(parts[1]) {
			orgs[account][parts[1]] = true
		}
		live = append(live, key+"\x00"+entry.Token+"\x00"+strconv.FormatInt(entry.ExpiresAt, 10))
	}
	for account := range orgs {
		state.Accounts = append(state.Accounts, account)
	}
	sort.Strings(state.Accounts)
	if len(state.Accounts) == 1 {
		state.Account = state.Accounts[0]
		if len(orgs[state.Account]) == 1 {
			for org := range orgs[state.Account] {
				state.Org = org
			}
		}
	}
	state.Entries = len(live)
	if len(live) > 0 {
		sort.Strings(live)
		sum := sha256.Sum256([]byte(strings.Join(live, "\x01")))
		state.Fingerprint = hex.EncodeToString(sum[:])
	}
	return state, nil
}

// savedSignIn is what the offline check learned about one saved sign-in.
type savedSignIn struct {
	// Empty means the copy holds no live entry: Claude will have to renew the
	// sign-in from its web session, or ask for a fresh sign-in.
	Empty   bool
	SavedAt time.Time
}

// checkSavedSignIn checks a saved sign-in before it is put in place, without
// the network: the copy must be complete (its seal matches), and it must
// belong to the account whose folder it sits in.
func checkSavedSignIn(dir, account string, decrypt cacheDecrypter) (savedSignIn, error) {
	var saved savedSignIn
	configPath := filepath.Join(dir, "config.json")
	info, err := os.Lstat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return saved, coded("NO_SAVED_SIGNIN", "there is no saved sign-in for that account; sign in to it in Claude and use Add account")
	}
	if err != nil {
		return saved, codedf("SAVED_SIGNIN_DAMAGED", "the saved sign-in for that account could not be read (%w)", err)
	}
	saved.SavedAt = info.ModTime()
	if err := validateSavedProfile(dir); err != nil {
		return saved, coded("SAVED_SIGNIN_DAMAGED", "the saved sign-in for that account is incomplete or was changed; sign in to it in Claude again")
	}
	state, err := readSignIn(configPath, decrypt)
	if err != nil {
		return saved, err
	}
	cfg, err := readObject(configPath)
	if err != nil {
		return saved, codedf("SAVED_SIGNIN_DAMAGED", "the saved sign-in for that account could not be read (%w)", err)
	}
	var remembered string
	if raw, ok := cfg.get("lastKnownAccountUuid"); ok {
		_ = json.Unmarshal(raw, &remembered)
	}
	mismatch := coded("SAVED_SIGNIN_MISMATCH", "the saved sign-in in that account's folder belongs to a different account; sign in to the right account in Claude again")
	if state.Multiple() || (state.Account != "" && state.Account != account) || (remembered != "" && remembered != account) {
		return saved, mismatch
	}
	saved.Empty = state.SignedOut()
	return saved, nil
}
