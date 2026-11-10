package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClaudeBlockersKnowWhatBelongsToClaude(t *testing.T) {
	const data = "/Users/someone/Library/Application Support/Claude"
	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"the app":                         {"/Applications/Claude.app/Contents/MacOS/Claude", "Claude"},
		"a helper":                        {"/Applications/Claude.app/Contents/Frameworks/Claude Helper (Renderer).app/Contents/MacOS/Claude Helper (Renderer)", "Claude"},
		"a copy in the user's own folder": {"/Users/someone/Applications/Claude.app/Contents/MacOS/Claude", "Claude"},
		"the wrapper around a task":       {"/Applications/Claude.app/Contents/Helpers/disclaimer", "a Claude Code task"},
		"a Claude Code task":              {data + "/claude-code/2.1.321/e6736b2d954d/claude.app/Contents/MacOS/claude", "a Claude Code task"},
		"the updater inside the app":      {"/Applications/Claude.app/Contents/Frameworks/Squirrel.framework/Resources/ShipIt", "Claude's updater"},
		"the updater's working copy":      {"/Users/someone/Library/Caches/com.anthropic.claudefordesktop.ShipIt/ShipIt", "Claude's updater"},
		"crash reporter (lingers, idle)":  {"/Applications/Claude.app/Contents/Frameworks/Electron Framework.framework/Helpers/chrome_crashpad_handler", ""},
		"the browser extension's bridge":  {"/Applications/Claude.app/Contents/Helpers/chrome-native-host", ""},
		"the command-line Claude":         {"/opt/homebrew/bin/claude", ""},
		"another app's updater":           {"/Users/someone/Applications/Slack.app/Contents/Frameworks/Squirrel.framework/Resources/ShipIt", ""},
		"another app's crash reporter":    {"/Applications/Kimi.app/Contents/Frameworks/Electron Framework.framework/Helpers/chrome_crashpad_handler", ""},
		"this helper":                     {"/Users/someone/Applications/Claude Swap Command.app/Contents/MacOS/claude-swap-command", ""},
		"an app that merely contains it":  {"/Applications/Claude Notes.app/Contents/MacOS/Claude Notes", ""},
	} {
		got := claudeBlockers([]procEntry{{PID: 10, Parent: 1, Path: tc.path}}, data)
		switch {
		case tc.want == "" && len(got) != 0:
			t.Errorf("%s: must not count as Claude still running, got %v", name, got)
		case tc.want != "" && (len(got) != 1 || got[0] != tc.want):
			t.Errorf("%s: blockers = %v, want [%s]", name, got, tc.want)
		}
	}
	both := claudeBlockers([]procEntry{
		{PID: 1, Path: "/Applications/Claude.app/Contents/MacOS/Claude"},
		{PID: 2, Path: "/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper"},
		{PID: 3, Path: data + "/claude-code/2.1.321/x/claude.app/Contents/MacOS/claude"},
	}, data)
	if fmt.Sprint(both) != "[Claude a Claude Code task]" {
		t.Fatalf("each kind is named once, the app first: %v", both)
	}
}

// closeScript plays out what Claude does, one entry per poll.
type closeScript struct {
	t        *testing.T
	polls    [][]string // what is still running at each poll; the last entry repeats
	failList bool
	marks    []string // what quietMarks returns at each poll; the last entry repeats
	now      time.Time
	asked    int
	poll     int
	waits    [][]string
	cancelAt int
}

func (s *closeScript) env() closeEnv {
	s.now = time.Unix(1_800_000_000, 0)
	at := func(list [][]string, i int) []string {
		if len(list) == 0 {
			return nil
		}
		if i >= len(list) {
			i = len(list) - 1
		}
		return list[i]
	}
	return closeEnv{
		blockers: func(context.Context) ([]string, error) {
			if s.failList {
				return nil, errors.New("fixture: process list unavailable")
			}
			return at(s.polls, s.poll), nil
		},
		requestQuit: func() error { s.asked++; return nil },
		quietMarks: func() string {
			if len(s.marks) == 0 {
				return "unchanged"
			}
			i := s.poll
			if i >= len(s.marks) {
				i = len(s.marks) - 1
			}
			return s.marks[i]
		},
		sleep: func(_ context.Context, d time.Duration) error {
			s.poll++
			s.now = s.now.Add(d)
			if s.cancelAt > 0 && s.poll >= s.cancelAt {
				return context.Canceled
			}
			return nil
		},
		now:    func() time.Time { return s.now },
		onWait: func(waiting []string) { s.waits = append(s.waits, waiting) },
		poll:   time.Second, quiet: 5 * time.Second, limit: 5 * time.Minute,
	}
}

func TestCloseAsksOnceAndWaitsUntilClaudeHasBeenGoneForAWhile(t *testing.T) {
	app, task := []string{"Claude"}, []string{"a Claude Code task"}
	s := &closeScript{t: t, polls: [][]string{app, app, task, nil}}
	started := s.env()
	if err := closeClaudeAndWait(context.Background(), started); err != nil {
		t.Fatal(err)
	}
	if s.asked != 1 {
		t.Fatalf("Claude is asked to quit once, not nagged: %d", s.asked)
	}
	if gone := s.now.Sub(time.Unix(1_800_000_000, 0)); gone < 8*time.Second {
		t.Fatalf("closed only counts after 5 quiet seconds: returned after %v", gone)
	}
	if len(s.waits) == 0 || fmt.Sprint(s.waits[len(s.waits)-1]) != "[a Claude Code task]" {
		t.Fatalf("the caller is told what it is waiting for: %v", s.waits)
	}
}

func TestCloseDoesNotAskWhenClaudeIsAlreadyClosed(t *testing.T) {
	s := &closeScript{t: t}
	if err := closeClaudeAndWait(context.Background(), s.env()); err != nil || s.asked != 0 {
		t.Fatalf("nothing to ask: asked=%d err=%v", s.asked, err)
	}
}

// When Claude quits with an update waiting, its updater installs it and
// starts Claude again by itself. That Claude has to be asked to quit too.
func TestCloseAsksAgainWhenClaudesUpdaterReopensIt(t *testing.T) {
	app, updater := []string{"Claude"}, []string{"Claude's updater"}
	s := &closeScript{t: t, polls: [][]string{app, updater, updater, nil, nil, app, app, nil}}
	if err := closeClaudeAndWait(context.Background(), s.env()); err != nil {
		t.Fatal(err)
	}
	if s.asked != 2 {
		t.Fatalf("the reopened Claude must be asked again: asked=%d", s.asked)
	}
}

func TestCloseGivesUpWithoutNaggingWhenClaudeStaysOpen(t *testing.T) {
	s := &closeScript{t: t, polls: [][]string{{"Claude", "a Claude Code task"}}}
	err := closeClaudeAndWait(context.Background(), s.env())
	if errorCode(err) != "CLAUDE_STILL_OPEN" || s.asked != 1 {
		t.Fatalf("want CLAUDE_STILL_OPEN after one request, got %v (asked %d)", err, s.asked)
	}
	if !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "a Claude Code task") {
		t.Fatalf("the message says nothing changed and what was still running: %v", err)
	}
	if waited := s.now.Sub(time.Unix(1_800_000_000, 0)); waited < 5*time.Minute || waited > 6*time.Minute {
		t.Fatalf("gives up at the limit: %v", waited)
	}
}

func TestCloseStartsItsQuietTimeAgainWhenClaudesFilesChange(t *testing.T) {
	s := &closeScript{t: t, marks: []string{"a", "a", "a", "b", "b", "b", "b", "b", "b", "b"}}
	if err := closeClaudeAndWait(context.Background(), s.env()); err != nil {
		t.Fatal(err)
	}
	if gone := s.now.Sub(time.Unix(1_800_000_000, 0)); gone < 8*time.Second {
		t.Fatalf("a write to Claude's sign-in files restarts the quiet time: returned after %v", gone)
	}
}

func TestCloseCanBeCancelledAndTreatsAnUnknownProcessListAsOpen(t *testing.T) {
	s := &closeScript{t: t, polls: [][]string{{"Claude"}}, cancelAt: 3}
	if err := closeClaudeAndWait(context.Background(), s.env()); errorCode(err) != "SWITCH_CANCELLED" {
		t.Fatalf("cancelling: %v", err)
	}
	unknown := &closeScript{t: t, failList: true}
	err := closeClaudeAndWait(context.Background(), unknown.env())
	if errorCode(err) != "CLAUDE_STILL_OPEN" || unknown.asked != 0 {
		t.Fatalf("an unreadable process list is never treated as closed: %v (asked %d)", err, unknown.asked)
	}
}

// One look at whether Claude is closed must not depend on how much time the
// switch has left, and one failed look at the process list is asked again
// before it counts as "not closed".
func TestOneLookAtClosedRetriesOnceAndHasItsOwnTimeLimit(t *testing.T) {
	const data = "/Users/someone/Library/Application Support/Claude"
	calls := 0
	list := func(ctx context.Context) ([]procEntry, error) {
		calls++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if calls == 1 {
			return nil, errors.New("fixture: the process list failed once")
		}
		return []procEntry{{PID: 1, Path: "/sbin/launchd"}}, nil
	}
	if !closedNow(list, data) || calls != 2 {
		t.Fatalf("a single failure is asked again: calls=%d", calls)
	}
	calls = 0
	failing := func(context.Context) ([]procEntry, error) { calls++; return nil, errors.New("fixture: down") }
	if closedNow(failing, data) || calls != 2 {
		t.Fatalf("not knowing is never closed, and is asked twice only: calls=%d", calls)
	}
	running := func(context.Context) ([]procEntry, error) {
		return []procEntry{{PID: 2, Path: "/Applications/Claude.app/Contents/MacOS/Claude"}}, nil
	}
	if closedNow(running, data) {
		t.Fatal("a running Claude is not closed")
	}
}
