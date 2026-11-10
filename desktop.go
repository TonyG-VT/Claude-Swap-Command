// Modified for Claude Swap Command (2026).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// desktop describes the data folder of the Claude desktop app.
type desktop struct{ dir string }

func (d desktop) configPath() string  { return filepath.Join(d.dir, "config.json") }
func (d desktop) sessionsDir() string { return filepath.Join(d.dir, "claude-code-sessions") }
func (d desktop) spacesDir() string   { return filepath.Join(d.dir, "local-agent-mode-sessions") }

// cookieFiles hold the claude.ai web session. They are locked while the app runs.
// Newer Chromium builds keep them in Network/, the macOS app keeps them in the
// data folder itself.
func (d desktop) cookieFiles() []string {
	base := d.dir
	if _, err := os.Stat(filepath.Join(d.dir, "Network")); err == nil {
		base = filepath.Join(d.dir, "Network")
	}
	return []string{
		filepath.Join(base, "Cookies"),
		filepath.Join(base, "Cookies-journal"),
		filepath.Join(base, "Cookies-wal"),
		filepath.Join(base, "Cookies-shm"),
	}
}

// authKeys are the config.json entries that belong to the signed-in account.
// The optional stale-session latch is session-cookie-bound re-login evidence.
// Keep it with that account, including when returning to an earlier sign-in.
// Everyday revoke-owed queues belong to the app: restoring an account must not
// clear or replace pending revocations, including those from other accounts.
var authKeys = []string{"oauth:tokenCache", "oauth:tokenCacheV2", "lastKnownAccountUuid", "oauth:staleSessionLatch"}

// tokenCache returns the encrypted oauth:tokenCacheV2 value of a config file.
func tokenCacheOf(configPath string) (string, error) {
	cfg, err := readObject(configPath)
	if err != nil {
		return "", err
	}
	raw, ok := cfg.get("oauth:tokenCacheV2")
	if !ok {
		return "", errors.New("no saved sign-in")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// object is a JSON object that keeps its key order, so config.json only
// changes where we touch it.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func readObject(path string) (*object, error) {
	data, _, err := readStableFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON object in %s", path)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%s: not a JSON object", path)
	}
	o := &object{vals: map[string]json.RawMessage{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.set(tok.(string), v)
	}
	return o, nil
}

func (o *object) get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// write saves the object the way the app does: tab indented, no trailing newline.
func (o *object) write(path string) error {
	data, err := o.bytes()
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// bytes is the object as the app writes it.
func (o *object) bytes() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "\t"); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeFileAtomic(path string, data []byte) error {
	return writeProtectedFile(path, data, nil)
}

// copyFile copies src to dst. A missing src removes dst, so a snapshot
// restores exactly the files it had.
func copyFile(src, dst string) error {
	if err := validateActiveTransactionPath(src); err != nil {
		return err
	}
	data, state, err := readStableFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return removeProtectedFile(dst)
	}
	if err != nil {
		return err
	}
	return writeProtectedFile(dst, data, &state)
}
