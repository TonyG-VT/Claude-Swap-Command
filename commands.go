package main

// The command-line options that print something and exit, and the one-time
// copy of the earlier helper's settings. None of them changes Claude.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"
)

// keepStateFromBefore keeps one exact copy of the settings file as the
// earlier helper left it, the first time this version starts.
func keepStateFromBefore(dir string) error {
	kept := filepath.Join(dir, "state.json.before-"+version)
	if _, err := os.Lstat(kept); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeFileAtomic(kept, data)
}

// printDoctor runs the self-test against the installed Claude.
func printDoctor(out io.Writer) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	check := runSelfCheck(ctx, liveSelfCheckEnv())
	fmt.Fprintf(out, "Claude Swap Command %s\n%s\n", version, check.summary())
	for _, probe := range check.Probes {
		mark := "ok  "
		if !probe.OK {
			mark = "FAIL"
		}
		line := fmt.Sprintf("  %s %s: %s", mark, probe.Name, probe.Detail)
		if probe.Code != "" {
			line += " [" + probe.Code + "]"
		}
		fmt.Fprintln(out, line)
	}
	return check.blocker() == nil
}

// printStatus prints what the Status window shows, from what is saved on
// this Mac. It asks the usage service nothing.
func printStatus(dir string, out io.Writer) {
	s := newSwitcher(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s.noteSelfCheck(s.env.selfCheck(ctx))
	s.noticeSignIn()
	fmt.Fprint(out, s.statusReport(time.Now()))
}

func printDryRun(dir string, out io.Writer) bool {
	s := newSwitcher(dir)
	report, ok := s.dryRunReport(time.Now())
	fmt.Fprint(out, report)
	return ok
}

// dryRunReport says what a switch to each other account would carry over,
// and whether each saved sign-in could be put in place. It reads only.
// Conversation titles are left out: the report may be pasted elsewhere.
func (s *switcher) dryRunReport(now time.Time) (string, bool) {
	var b strings.Builder
	ok := true
	d := s.env.desktop()
	state, err := readSignIn(d.configPath(), s.env.decrypt)
	s.mu.Lock()
	accounts := make([]Account, 0, len(s.st.Accounts))
	for _, a := range s.st.Accounts {
		accounts = append(accounts, *a)
	}
	s.mu.Unlock()
	number := func(uuid string) int {
		for i, a := range accounts {
			if a.UUID == uuid {
				return i + 1
			}
		}
		return 0
	}
	var source *Account
	switch {
	case err != nil:
		fmt.Fprintf(&b, "Claude's sign-in could not be read [%s]: %s\n", errorCode(err), sanitizeError(err))
		ok = false
	case state.Multiple():
		b.WriteString("Claude has more than one account signed in at once [MULTIPLE_ACCOUNTS_SIGNED_IN]\n")
		ok = false
	case state.SignedOut():
		b.WriteString("Claude is signed out, so a switch would carry no conversations.\n")
	case number(state.Account) == 0:
		b.WriteString("Claude is signed in to an account that is not in the menu yet.\n")
	default:
		source = &accounts[number(state.Account)-1]
		fmt.Fprintf(&b, "Claude is signed in to Account %d\n", number(state.Account))
	}
	for i, target := range accounts {
		if target.Retired || (source != nil && target.UUID == source.UUID) {
			continue
		}
		if source != nil {
			plan, err := planCarry(d, carryNamespace{source.UUID, source.Org}, carryNamespace{target.UUID, target.Org}, nil)
			if err != nil {
				fmt.Fprintf(&b, "Account %d → Account %d: conversations would not be carried [%s]: %s\n", number(source.UUID), i+1, errorCode(err), sanitizeError(err))
			} else {
				summary := plan.summary()
				fmt.Fprintf(&b, "Account %d → Account %d: %s\n", number(source.UUID), i+1, carryCountsText(summary))
				reasons := map[string]int{}
				for _, skipped := range summary.Skipped {
					reasons[skipped.Code]++
				}
				for code, n := range reasons {
					fmt.Fprintf(&b, "    %d would be left as they are [%s]: %s\n", n, code, skipReason(code))
				}
				if len(summary.Ignored) > 0 {
					fmt.Fprintf(&b, "    files the helper does not know: %s\n", strings.Join(summary.Ignored, ", "))
				}
				if len(summary.NewFields) > 0 {
					fmt.Fprintf(&b, "    new kinds of details (copied exactly): %s\n", strings.Join(summary.NewFields, ", "))
				}
			}
		}
		saved, err := checkSavedSignIn(s.profileDir(target.UUID), target.UUID, s.env.decrypt)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "  saved sign-in for Account %d: NOT USABLE [%s]: %s\n", i+1, errorCode(err), sanitizeError(err))
			ok = false
		case saved.Empty:
			fmt.Fprintf(&b, "  saved sign-in for Account %d: complete and its own, saved %s; it holds no tokens, so Claude will renew it from the web session\n", i+1, ageText(now.Sub(saved.SavedAt)))
		default:
			fmt.Fprintf(&b, "  saved sign-in for Account %d: complete and its own, saved %s\n", i+1, ageText(now.Sub(saved.SavedAt)))
		}
	}
	if pending, err := pendingBackups(d, s.backupsDir()); err != nil || len(pending) > 0 {
		b.WriteString("An earlier switch was interrupted; the menu offers Fix an interrupted switch…\n")
	}
	return b.String(), ok
}

// runUISelfTest shows each kind of window the helper uses for a moment, so a
// broken window is found before it is needed.
func runUISelfTest(out io.Writer) {
	systray.Run(func() {
		systray.SetIcon(trayIcon(-1, defaultThreshold))
		systray.SetTooltip(appTitle + " (window test)")
		go func() {
			results := []string{}
			note := func(name string, got int) {
				results = append(results, fmt.Sprintf("%s: %s", name, map[bool]string{true: "shown and closed by itself", false: fmt.Sprintf("unexpected result %d", got)}[got == -1]))
			}
			note("question window", showAlert(alertInfo, "Window test 1 of 3", "This window closes by itself in a moment. You do not need to do anything.", "", 2*time.Second, "Switch", "Cancel"))
			note("problem window", showAlert(alertWarning, "Window test 2 of 3", "This window closes by itself in a moment.\n\nCode: EXAMPLE", "", 2*time.Second, "OK"))
			note("status window", showAlert(alertInfo, appTitle, "", "Window test 3 of 3\n\nThis is where Status… shows its text.\nIt closes by itself in a moment.", 2*time.Second, "Close", "Copy", "Open data folder"))
			for _, line := range results {
				fmt.Fprintln(out, line)
			}
			systray.Quit()
		}()
	}, func() {})
}
