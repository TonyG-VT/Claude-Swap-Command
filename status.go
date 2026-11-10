package main

// The text of the Status window, and of -status and -doctor on the command
// line. It names accounts by their menu number, name and email, never by
// token, cookie or full identifier.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var skipReasons = map[string]string{
	"RECORD_UNREADABLE":          "its file could not be read",
	"RECORD_MALFORMED":           "its file is not in a form the helper understands",
	"QUEUED_MESSAGE":             "it has a message waiting to be sent",
	"REMOTE_RECORD":              "it is a remote session, which stays with its own account",
	"STILL_IMPORTING":            "Claude is still importing it",
	"DELETED_ON_DESTINATION":     "it was deleted on the account you switched to",
	"TRANSCRIPT_OWNED_ELSEWHERE": "another conversation on that account already uses the same transcript",
	"DESTINATION_BRANCHED":       "the copy on that account has continued separately",
	"NAMED_IN_UNKNOWN_FILE":      "a file the helper does not know mentions it",
	"DESTINATION_UNREADABLE":     "the copy on that account could not be read",
}

// skipReason says in plain words why a conversation was left as it was.
func skipReason(code string) string {
	if reason := skipReasons[code]; reason != "" {
		return reason
	}
	return "it could not be carried safely (" + code + ")"
}

var verdictTexts = map[switchVerdict]string{
	verdictConfirmed:        "Claude accepted the sign-in",
	verdictUnconfirmed:      "not confirmed yet: Claude had not shown which account it is on when the helper stopped watching",
	verdictNeedsSignIn:      "Claude did not accept the saved sign-in and asked to sign in again",
	verdictDifferentAccount: "Claude ended up on a different account",
}

// carryCountsText turns the counts of one carry-over into a sentence part:
// "3 new, 1 updated, 15 already the same".
func carryCountsText(c *journalCarry) string {
	var parts []string
	add := func(n int, text string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, text))
		}
	}
	add(c.New, "new")
	add(c.Updated, "updated")
	add(c.Refreshed, "brought in line")
	add(c.Identical, "already the same")
	add(c.KeptNewer, "kept because the copy there is newer")
	add(c.KeptCache, "kept because only cache details differ")
	if len(parts) == 0 {
		return "nothing to carry"
	}
	return strings.Join(parts, ", ")
}

// lastManualSwitch reads the newest finished switch from the journal.
func (j *switchJournal) lastManualSwitch() *journalEntry {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var last *journalEntry
	for _, name := range []string{"switches.1.jsonl", "switches.jsonl"} {
		data, err := os.ReadFile(filepath.Join(j.dir, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var e journalEntry
			if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Kind == "manual_switch" && e.Verdict != "" {
				entry := e
				last = &entry
			}
		}
	}
	return last
}

func (s *switcher) accountNameLocked(uuid string) string {
	if uuid == "" {
		return "signed out"
	}
	return s.accountShortLocked(uuid)
}

// lastSwitchText describes the last switch: from memory when this run made it
// (with conversation titles), otherwise from the journal (without).
func (s *switcher) lastSwitchText(now time.Time) string {
	s.mu.Lock()
	outcome := s.lastOutcome
	s.mu.Unlock()
	var b strings.Builder
	head := func(from, to string, at time.Time, verdict switchVerdict) {
		s.mu.Lock()
		fromName, toName := s.accountNameLocked(from), s.accountNameLocked(to)
		s.mu.Unlock()
		fmt.Fprintf(&b, "Last switch: %s → %s, %s\n", fromName, toName, resetText(at, now))
		if text := verdictTexts[verdict]; text != "" {
			fmt.Fprintf(&b, "  %s\n", text)
		}
	}
	tail := func(c *journalCarry) {
		if len(c.Ignored) > 0 {
			fmt.Fprintf(&b, "  Files the helper does not know were left alone: %s\n", strings.Join(c.Ignored, ", "))
		}
		if len(c.NewFields) > 0 {
			fmt.Fprintf(&b, "  New kinds of details Claude now keeps (copied exactly, nothing to do): %s\n", strings.Join(c.NewFields, ", "))
		}
	}
	if outcome != nil {
		head(outcome.From, outcome.To, outcome.At, outcome.Verdict)
		switch {
		case outcome.Carry != nil:
			summary := outcome.Carry.summary()
			fmt.Fprintf(&b, "  Conversations: %s\n", carryCountsText(summary))
			for _, skipped := range outcome.Carry.Skipped {
				name := `"` + skipped.Title + `"`
				if skipped.Title == "" {
					name = "conversation " + shortRecordID(skipped.Name)
				}
				fmt.Fprintf(&b, "  Left as it was: %s: %s\n", name, skipReason(skipped.Code))
			}
			tail(summary)
		case outcome.From != "" && outcome.From != outcome.To:
			fmt.Fprintf(&b, "  Conversations were not carried over: %s\n", outcome.CarryNote)
		}
		if outcome.GroupsNote != "" {
			fmt.Fprintf(&b, "  Project groups were not carried over: %s\n", outcome.GroupsNote)
		}
		return b.String()
	}
	entry := s.journal.lastManualSwitch()
	if entry == nil {
		return ""
	}
	head(entry.From, entry.To, entry.At, switchVerdict(entry.Verdict))
	if c := entry.Carry; c != nil {
		if c.Outcome == "none" {
			fmt.Fprintf(&b, "  Conversations were not carried over: %s\n", c.Reason)
		} else {
			fmt.Fprintf(&b, "  Conversations: %s\n", carryCountsText(c))
			reasons := map[string]int{}
			for _, skipped := range c.Skipped {
				reasons[skipReason(skipped.Code)]++
			}
			var lines []string
			for reason, n := range reasons {
				lines = append(lines, fmt.Sprintf("  %d left as %s (%s)\n", n, map[bool]string{true: "it was", false: "they were"}[n == 1], reason))
			}
			sort.Strings(lines)
			b.WriteString(strings.Join(lines, ""))
			tail(c)
		}
	}
	return b.String()
}

// statusReport is everything the Status window says.
func (s *switcher) statusReport(now time.Time) string {
	snap := s.snapshot()
	s.mu.Lock()
	check := s.lastSelfCheck
	s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Claude Swap Command %s\n\n", version)

	if check != nil {
		b.WriteString(check.summary() + "\n")
		failing := false
		for _, probe := range check.Probes {
			failing = failing || !probe.OK
		}
		if failing {
			for _, probe := range check.Probes {
				mark := "✓"
				if !probe.OK {
					mark = "✗"
				}
				fmt.Fprintf(&b, "  %s %s: %s\n", mark, probe.Name, probe.Detail)
			}
		}
		b.WriteString("\n")
	}

	switch {
	case snap.Busy && snap.Status != "":
		fmt.Fprintf(&b, "Right now: %s\n\n", snap.Status)
	case snap.Status != "":
		fmt.Fprintf(&b, "%s\n\n", snap.Status)
	}
	if snap.Blocked {
		b.WriteString("An earlier switch was interrupted. Choose Fix an interrupted switch… in the menu.\n\n")
	}

	if len(snap.Accounts) == 0 {
		b.WriteString("No accounts yet. Open Claude and sign in; the account appears in the menu by itself. Then use Add account… for the next one.\n")
	}
	for i, a := range snap.Accounts {
		mark := "○"
		if a.UUID == snap.Active {
			mark = "●"
		}
		title := accountTitle(i+1, a)
		if a.Retired {
			title += " · retired"
		}
		fmt.Fprintf(&b, "%s %s\n    %s\n", mark, title, usageDetail(a, snap.Threshold, now))
	}
	switch {
	case len(snap.Accounts) == 0:
	case snap.Adding:
		b.WriteString("\nWaiting for you to sign in to the new account in Claude.\n")
	case snap.Active == "":
		b.WriteString("\nClaude is signed out.\n")
	}

	if text := s.lastSwitchText(now); text != "" {
		b.WriteString("\n" + text)
	}
	if len(snap.OldSignIns) > 0 {
		b.WriteString("\nConversations filed under an old sign-in (import them from the menu):\n")
		for _, o := range snap.OldSignIns {
			fmt.Fprintf(&b, "  %s\n", o.label())
		}
	}
	if snap.Paused {
		b.WriteString("\nUsage checks are paused. Switching still works.\n")
	}
	fmt.Fprintf(&b, "\nData folder: %s\n", s.dir)
	return b.String()
}

// switchMessage is the short message shown after a switch that worked.
func (s *switcher) switchMessage() (title, body string) {
	s.mu.Lock()
	o := s.lastOutcome
	if o == nil {
		s.mu.Unlock()
		return "Switched", ""
	}
	title = "Now on " + s.accountShortLocked(o.To)
	s.mu.Unlock()
	switch {
	case o.Carry != nil:
		summary := o.Carry.summary()
		if n := len(o.Carry.writes); n > 0 {
			var parts []string
			for _, part := range []struct {
				n    int
				text string
			}{{summary.New, "new"}, {summary.Updated, "updated"}, {summary.Refreshed, "brought in line"}} {
				if part.n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", part.n, part.text))
				}
			}
			body = fmt.Sprintf("%s came with you (%s).", plural(n, "conversation", "conversations"), strings.Join(parts, ", "))
		} else {
			body = "Your conversations were already up to date."
		}
		if n := len(o.Carry.Skipped); n == 1 {
			body += " 1 was left as it was; Status… says why."
		} else if n > 1 {
			body += fmt.Sprintf(" %d were left as they were; Status… says why.", n)
		}
	case o.From == "":
		body = "Claude was signed out before, so there were no conversations to carry over."
	case o.From != o.To:
		body = "The account switched, but your conversations were not carried over: " + o.CarryNote + ". Status… has the details."
	}
	if o.Verdict == verdictUnconfirmed {
		body = strings.TrimSpace(body + " Claude has not confirmed the account yet; check that it shows the right one.")
	}
	return title, body
}
