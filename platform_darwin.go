// Modified for Claude Swap Command (2026).
package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func claudeDesktop() desktop {
	home, _ := os.UserHomeDir()
	return desktop{dir: filepath.Join(home, "Library", "Application Support", "Claude")}
}

func startDesktop(string) error {
	return exec.Command("/usr/bin/open", "-a", claudeBundlePath()).Run()
}

// shellOpen opens a folder, or a web page in the default browser.
func shellOpen(target string) { _ = exec.Command("open", target).Start() }

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.tonyg-vt.claude-swap-command.plist")
}

func autostartEnabled() bool {
	exe, err := installedSwitcherExecutable()
	if err != nil {
		return false
	}
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :ProgramArguments:0", launchAgentPath()).Output()
	return err == nil && strings.TrimSpace(string(out)) == exe
}

func setAutostart(on bool) error {
	if !on {
		err := os.Remove(launchAgentPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := installedSwitcherExecutable()
	if err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.tonyg-vt.claude-swap-command</string>
	<key>ProgramArguments</key><array><string>%s</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`, html.EscapeString(exe))
	if err := os.MkdirAll(filepath.Dir(launchAgentPath()), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(launchAgentPath(), []byte(plist))
}

// A launch-at-login registration must point to the stable installed bundle,
// not a build checkout, mounted disk image, Downloads copy or translocated app.
func installedSwitcherExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", errors.New("the installed app could not be located")
	}
	if err := validateInstalledSwitcherPath(resolved, home); err != nil {
		return "", err
	}
	return resolved, nil
}

func validateInstalledSwitcherPath(exe, home string) error {
	for _, base := range []string{"/Applications", filepath.Join(home, "Applications")} {
		want := filepath.Join(base, "Claude Swap Command.app", "Contents", "MacOS", "claude-swap-command")
		if filepath.Clean(exe) == want && filepath.IsAbs(exe) {
			return nil
		}
	}
	return errors.New("Install Claude Swap Command in Applications and open that copy before enabling Start at login")
}
