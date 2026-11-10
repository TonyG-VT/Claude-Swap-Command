package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Anthropic's signature: Apple-anchored, Claude's identifier, Anthropic's team.
const claudeVendorRequirement = `=anchor apple generic and identifier "com.anthropic.claudefordesktop" and certificate leaf[subject.OU] = "Q6L2SF6YDW"`

// claudeBundlePath is where Claude is installed. /Applications is the normal
// place; a copy in the user's own Applications folder is accepted too.
func claudeBundlePath() string {
	if home, err := os.UserHomeDir(); err == nil {
		own := filepath.Join(home, "Applications", "Claude.app")
		if _, err := os.Stat("/Applications/Claude.app"); err != nil {
			if _, err := os.Stat(own); err == nil {
				return own
			}
		}
	}
	return "/Applications/Claude.app"
}

func systemCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = 250 * time.Millisecond
	return cmd.Output()
}

// currentDesktopVersion is the installed Claude's version, such as
// "4.23118.1 (4.23118.1)", or "" when it cannot be read.
func currentDesktopVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	plist := filepath.Join(claudeBundlePath(), "Contents", "Info.plist")
	out, err := systemCommand(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString", plist)
	if err != nil {
		return ""
	}
	build, err := systemCommand(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleVersion", plist)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out)) + " (" + strings.TrimSpace(string(build)) + ")"
}

// verifyClaudeVendor checks that the installed Claude.app is intact and signed
// by Anthropic. It says nothing about which version it is.
func verifyClaudeVendor(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := systemCommand(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--all-architectures", "-R", claudeVendorRequirement, claudeBundlePath())
	return err
}

// claudeCookieStores lists the cookie databases in Claude's data folder that
// hold the claude.ai session cookie. Only the cookie's name and host are
// matched; no value is read. The databases are opened read-only and immutable
// so a running Claude is not disturbed.
func claudeCookieStores(ctx context.Context, d desktop) ([]string, error) {
	const sqlite = "/usr/bin/sqlite3"
	if _, err := os.Stat(sqlite); err != nil {
		return nil, errors.New("sqlite3 is not available")
	}
	candidates := []string{filepath.Join(d.dir, "Cookies"), filepath.Join(d.dir, "Network", "Cookies")}
	for _, pattern := range []string{filepath.Join(d.dir, "Partitions", "*", "Cookies"), filepath.Join(d.dir, "Partitions", "*", "Network", "Cookies")} {
		more, _ := filepath.Glob(pattern)
		candidates = append(candidates, more...)
	}
	var stores []string
	inspected := 0
	for _, path := range candidates {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
		out, err := systemCommand(queryCtx, sqlite, "-readonly", uri,
			"select count(*) from cookies where name = 'sessionKey' and host_key like '%claude.ai';")
		cancel()
		if err != nil {
			continue
		}
		inspected++
		if count := strings.TrimSpace(string(out)); count != "" && count != "0" {
			stores = append(stores, path)
		}
	}
	if inspected == 0 {
		return nil, errors.New("no cookie database could be inspected")
	}
	return stores, nil
}

// liveSelfCheckEnv is the self-test's view of the real Claude on this Mac.
func liveSelfCheckEnv() selfCheckEnv {
	return selfCheckEnv{
		desktop:      claudeDesktop(),
		decrypt:      decryptValue,
		version:      currentDesktopVersion,
		verifyVendor: verifyClaudeVendor,
		cookieStores: claudeCookieStores,
	}
}
