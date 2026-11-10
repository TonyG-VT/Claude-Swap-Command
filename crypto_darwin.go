// Modified for Claude Swap Command (2026).
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"errors"
	"os/exec"
	"strings"
	"sync"
)

// On macOS the app encrypts with Chromium's scheme: a password in the login
// keychain ("Claude Safe Storage"), stretched with PBKDF2, and values prefixed
// "v10" encrypted with AES-128-CBC. macOS asks once whether the switcher may
// read that keychain item.

var (
	keyMu  sync.Mutex
	keyVal []byte
	// keychainPassword asks macOS for Claude's storage password. The call goes
	// through /usr/bin/security, so the keychain's "Always Allow" belongs to
	// that Apple tool and survives a rebuilt helper.
	keychainPassword = func() (string, error) {
		out, err := exec.Command("/usr/bin/security", "find-generic-password", "-w", "-s", "Claude Safe Storage").Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
)

// appKey derives Claude's storage key. A failure is never remembered: a
// keychain that was locked or denied is asked again on the next call.
func appKey() ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if keyVal != nil {
		return keyVal, nil
	}
	password, err := keychainPassword()
	if err != nil || password == "" {
		return nil, errKeychainUnavailable
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		return nil, errKeychainUnavailable
	}
	keyVal = key
	return keyVal, nil
}

func decryptValue(data []byte) ([]byte, error) {
	if len(data) < 3+aes.BlockSize || string(data[:3]) != "v10" {
		return nil, errors.New("unexpected encrypted value format")
	}
	key, err := appKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	ct := data[3:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(plain, ct)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
		return nil, errors.New("bad padding")
	}
	return plain[:len(plain)-pad], nil
}
