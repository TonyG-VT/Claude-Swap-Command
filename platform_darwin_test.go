package main

import (
	"path/filepath"
	"testing"
)

func TestAutostartRequiresStableInstalledApplication(t *testing.T) {
	home := "/Users/fixture-engineer"
	for _, base := range []string{"/Applications", filepath.Join(home, "Applications")} {
		exe := filepath.Join(base, "Claude Swap Command.app", "Contents", "MacOS", "claude-swap-command")
		if err := validateInstalledSwitcherPath(exe, home); err != nil {
			t.Fatalf("stable installation rejected: %v", err)
		}
	}
	for _, exe := range []string{
		"/Users/fixture-engineer/Downloads/Claude Swap Command.app/Contents/MacOS/claude-swap-command",
		"/Volumes/Claude Swap Command/Claude Swap Command.app/Contents/MacOS/claude-swap-command",
		"/private/var/folders/fixture/AppTranslocation/Claude Swap Command.app/Contents/MacOS/claude-swap-command",
		"/Users/fixture-engineer/project/dist/Claude Swap Command.app/Contents/MacOS/claude-swap-command",
		"/Applications/Other.app/Contents/MacOS/claude-swap-command",
		"/Applications/Claude Swap Command.app/Contents/Helpers/ClaudeSwapSetup",
		"/Applications/Claude Swap Command.app/Contents/Helpers/Claude Swap Command Setup.app/Contents/MacOS/ClaudeSwapSetup",
		"Applications/Claude Swap Command.app/Contents/MacOS/claude-swap-command",
		"/Users/somebody-else/Applications/Claude Swap Command.app/Contents/MacOS/claude-swap-command",
	} {
		if err := validateInstalledSwitcherPath(exe, home); err == nil {
			t.Errorf("unstable or unrelated executable allowed: %s", exe)
		}
	}
}
