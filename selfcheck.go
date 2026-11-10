package main

// The self-test replaces the old version lock.
//
// The helper used to accept only Claude builds whose program files matched
// hashes reviewed in advance, and only one macOS version, so every Claude
// update stopped all switching until someone reviewed it. The version is no
// longer part of the decision. Instead the helper checks, each time, the few
// things it actually relies on:
//
//   - Claude.app is Anthropic's own app (code signature);
//   - the sign-in can be read (signin.go), and one account at most is signed in;
//   - the sign-in cookie is in the file the helper swaps;
//   - the conversation folder can be read (needed only to carry conversations).
//
// Only the first three can stop a switch, and only because going ahead would
// do damage. Everything else is confirmed after the switch instead
// (verifyAfterLaunch).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const claudeBundleID = "com.anthropic.claudefordesktop"

type selfCheckProbe struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail"`
	blocks bool
}

type selfCheck struct {
	At            time.Time
	ClaudeVersion string
	SignIn        signInState
	Probes        []selfCheckProbe
	CanSwitch     bool // the sign-in can be saved and replaced
	CanCarry      bool // conversations can be carried
}

// selfCheckEnv is everything the self-test looks at. Tests supply their own.
type selfCheckEnv struct {
	desktop      desktop
	decrypt      cacheDecrypter
	version      func() string
	verifyVendor func(context.Context) error
	// cookieStores lists the cookie databases that hold Claude's web session
	// cookie (by name only; no value is read).
	cookieStores func(context.Context, desktop) ([]string, error)
}

func runSelfCheck(ctx context.Context, env selfCheckEnv) selfCheck {
	check := selfCheck{At: time.Now(), ClaudeVersion: env.version(), CanSwitch: true, CanCarry: true}
	pass := func(name, detail string) {
		check.Probes = append(check.Probes, selfCheckProbe{Name: name, OK: true, Detail: detail})
	}
	block := func(name, code, detail string) {
		check.Probes = append(check.Probes, selfCheckProbe{Name: name, Code: code, Detail: detail, blocks: true})
		check.CanSwitch = false
	}

	if err := env.verifyVendor(ctx); err != nil {
		block("Claude app", "CLAUDE_NOT_ANTHROPIC", "Claude.app could not be confirmed as Anthropic's own app")
	} else {
		pass("Claude app", "Claude.app is Anthropic's own app")
	}

	state, err := readSignIn(env.desktop.configPath(), env.decrypt)
	check.SignIn = state
	switch {
	case err != nil:
		block("Sign-in", errorCode(err), sanitizeError(err))
	case state.Multiple():
		block("Sign-in", "MULTIPLE_ACCOUNTS_SIGNED_IN", "Claude has more than one account signed in at once, which this helper cannot swap safely")
	case state.SignedOut():
		pass("Sign-in", "Claude is signed out")
	default:
		pass("Sign-in", "one account is signed in and its sign-in can be read")
	}

	expected := env.desktop.cookieFiles()[0]
	switch {
	case err != nil || state.Multiple():
		// Already blocked above; nothing more to learn here.
	case state.SignedOut():
		pass("Sign-in cookie", "nobody is signed in, so there is no session cookie to check")
	default:
		if info, statErr := os.Lstat(expected); statErr != nil || !info.Mode().IsRegular() {
			block("Sign-in cookie", "SIGNIN_COOKIES_MISSING", "Claude's sign-in cookie file is not where the helper expects it")
			break
		}
		stores, probeErr := env.cookieStores(ctx, env.desktop)
		moved := false
		for _, store := range stores {
			moved = moved || !samePath(store, expected)
		}
		switch {
		case probeErr != nil:
			pass("Sign-in cookie", "the cookie store could not be inspected; the switch is confirmed afterwards instead")
		case moved:
			block("Sign-in cookie", "SIGNIN_COOKIES_MOVED", "Claude now keeps its sign-in cookie in a different place than the helper swaps")
		case len(stores) == 0:
			pass("Sign-in cookie", "no session cookie was found under its usual name; the switch is confirmed afterwards instead")
		default:
			pass("Sign-in cookie", "the sign-in cookie is in the file the helper swaps")
		}
	}

	switch {
	case state.Account == "" || state.Org == "":
		pass("Conversations", "no conversation folder to read from the current sign-in")
	default:
		dir, pathErr := safeRelative(env.desktop.sessionsDir(), filepath.Join(state.Account, state.Org))
		var folder *carryFolder
		scanErr := pathErr
		if scanErr == nil {
			folder, scanErr = scanCarryFolder(dir)
		}
		if scanErr != nil {
			check.CanCarry = false
			check.Probes = append(check.Probes, selfCheckProbe{Name: "Conversations", Code: errorCode(withCode("CARRY_FOLDER_UNREADABLE", scanErr)),
				Detail: "conversations will not be carried: " + sanitizeError(withCode("CARRY_FOLDER_UNREADABLE", scanErr))})
		} else {
			pass("Conversations", fmt.Sprintf("%d conversations can be read", len(folder.records)))
		}
	}
	return check
}

// blocker is the reason switching is paused, or nil.
func (c selfCheck) blocker() error {
	for _, probe := range c.Probes {
		if !probe.OK && probe.blocks {
			return coded(probe.Code, probe.Detail)
		}
	}
	return nil
}

// summary is the one line shown in Status.
func (c selfCheck) summary() string {
	version := displayClaudeVersion(c.ClaudeVersion)
	if err := c.blocker(); err != nil {
		return "Claude " + version + ": switching is paused. " + sanitizeError(err)
	}
	for _, probe := range c.Probes {
		if !probe.OK {
			return "Claude " + version + ": self-test passed for switching, but " + probe.Detail
		}
	}
	return "Claude " + version + ": self-test passed"
}

// displayClaudeVersion turns "4.23118.1 (4.23118.1)" into "4.23118.1".
func displayClaudeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "(version unknown)"
	}
	if short, build, found := strings.Cut(v, " ("); found && strings.TrimSuffix(build, ")") == short {
		return short
	}
	return v
}
