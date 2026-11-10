package main

// Closing Claude for a switch, and knowing when it is really closed.
//
// The helper asks Claude to quit the normal way, exactly once. If work is
// still running Claude shows its own question and the person answers it
// there. Claude counts as closed only when none of its processes, none of the
// Claude Code tasks it started and not its updater have been running, and
// nothing has rewritten its sign-in files, for a quiet period.
//
// The updater matters: when Claude quits with an update waiting, the updater
// installs it and starts Claude again by itself. That Claude is asked to quit
// too, so the switch never runs underneath a Claude that reopened.

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

type procEntry struct {
	PID, Parent int
	Path        string
}

// claudeProcessKind says what a running program has to do with Claude, or ""
// when it does not stop a switch.
func claudeProcessKind(path, dataRoot string) string {
	base := filepath.Base(path)
	switch {
	case strings.Contains(path, "/"+claudeBundleID+".ShipIt/"):
		return "Claude's updater" // the updater's working copy outside the app
	case dataRoot != "" && strings.HasPrefix(path, dataRoot+"/claude-code"):
		return "a Claude Code task"
	case !strings.Contains(path, "/Claude.app/Contents/"):
		return ""
	case base == "ShipIt":
		return "Claude's updater"
	case strings.Contains(base, "crashpad_handler"):
		return "" // the crash reporter lingers after quit and touches nothing
	case base == "chrome-native-host":
		return "" // started by the browser for Claude's browser extension
	case base == "disclaimer":
		return "a Claude Code task"
	}
	return "Claude"
}

// claudeBlockers names, once each and the app first, what is still running.
func claudeBlockers(table []procEntry, dataRoot string) []string {
	seen := map[string]bool{}
	for _, p := range table {
		if kind := claudeProcessKind(p.Path, dataRoot); kind != "" {
			seen[kind] = true
		}
	}
	var out []string
	for _, kind := range []string{"Claude", "a Claude Code task", "Claude's updater"} {
		if seen[kind] {
			out = append(out, kind)
		}
	}
	return out
}

// closeEnv is what closing Claude needs from the outside world.
type closeEnv struct {
	blockers    func(context.Context) ([]string, error) // what is still running; an error means "unknown"
	requestQuit func() error                            // ask Claude to quit the normal way
	quietMarks  func() string                           // changes when Claude's sign-in files are rewritten
	sleep       func(context.Context, time.Duration) error
	now         func() time.Time
	onWait      func(waitingFor []string)
	poll        time.Duration
	quiet       time.Duration
	limit       time.Duration
}

const maxQuitRequests = 3

// closeClaudeAndWait returns nil once Claude has been fully closed for the
// quiet period. It changes nothing itself beyond asking Claude to quit.
func closeClaudeAndWait(ctx context.Context, env closeEnv) error {
	deadline := env.now().Add(env.limit)
	var quietSince time.Time
	lastMarks, asked, wasClosed := "", 0, false
	var last []string
	unknown := false
	for {
		blockers, err := env.blockers(ctx)
		unknown = err != nil
		if err == nil && len(blockers) == 0 {
			if marks := env.quietMarks(); quietSince.IsZero() || marks != lastMarks {
				quietSince, lastMarks = env.now(), marks
			}
			if env.now().Sub(quietSince) >= env.quiet {
				return nil
			}
			wasClosed = true
		} else {
			quietSince, last = time.Time{}, blockers
			appRunning := false
			for _, kind := range blockers {
				appRunning = appRunning || kind == "Claude"
			}
			// Ask once. Ask again only for a Claude that came back by itself
			// after having closed (its updater restarted it).
			if appRunning && (asked == 0 || wasClosed) && asked < maxQuitRequests {
				_ = env.requestQuit()
				asked++
				wasClosed = false
			}
			if env.onWait != nil {
				env.onWait(blockers)
			}
		}
		if !env.now().Before(deadline) {
			break
		}
		if err := env.sleep(ctx, env.poll); err != nil {
			return coded("SWITCH_CANCELLED", "The switch was cancelled, so nothing was changed.")
		}
	}
	if unknown {
		return coded("CLAUDE_STILL_OPEN", "The helper could not check whether Claude is closed, so nothing was changed.")
	}
	return codedf("CLAUDE_STILL_OPEN", "Claude did not close, so nothing was changed. Still running: %s. If Claude is asking a question, answer it there, then pick the account again.", strings.Join(last, ", "))
}

// closedNow is one look at whether Claude is closed right now. It has its own
// short time limit, so it does not depend on how much time the switch has
// left, and a process list that fails once is asked again. Not knowing is
// never treated as closed.
func closedNow(list func(context.Context) ([]procEntry, error), dataRoot string) bool {
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		table, err := list(ctx)
		cancel()
		if err == nil {
			return len(claudeBlockers(table, dataRoot)) == 0
		}
	}
	return false
}
