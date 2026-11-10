package main

// What the menu shows, worked out from a snapshot. Nothing here touches the
// menu bar itself (tray.go does), so every line of it can be tested.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	menuAccountSlots = 8
	menuImportSlots  = 4
	menuHeaderLimit  = 90
	// The header mentions the last switch for this long.
	menuOutcomeFor = 30 * time.Minute
	// Usage older than this is shown with its age.
	usageAgeShownAfter = 20 * time.Minute
)

type menuAccountRow struct {
	ID      string
	Title   string
	Detail  string
	Active  bool
	Enabled bool
}

type menuImportRow struct {
	Account, Org string
	Title        string
}

type menuModel struct {
	Header      string
	Accounts    []menuAccountRow
	AddTitle    string
	AddEnabled  bool
	Imports     []menuImportRow
	ShowFix     bool
	Paused      bool
	Tooltip     string
	IconPercent float64 // -1 when unknown
}

// accountTitle is how an account is named everywhere: "Account 2: Work
// (tony@example.com, Max)". The number is its place in the menu.
func accountTitle(number int, a Account) string {
	name := strings.TrimSpace(a.Alias)
	if name == "" {
		name = strings.TrimSpace(a.Name)
	}
	var details []string
	if name == "" {
		name = a.Email
	} else if a.Email != "" {
		details = append(details, a.Email)
	}
	if a.Plan != "" {
		details = append(details, a.Plan)
	}
	title := "Account " + strconv.Itoa(number)
	if name != "" {
		title += ": " + name
	}
	if len(details) > 0 {
		title += " (" + strings.Join(details, ", ") + ")"
	}
	return title
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ageText says how long ago something was: "5 minutes ago", "3 hours ago".
func ageText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute", "minutes") + " ago"
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour", "hours") + " ago"
	}
	return plural(int(d/(24*time.Hour)), "day", "days") + " ago"
}

// usageDetail is the line under an account: its numbers, how old they are
// when they are not fresh, and what is wrong with the check when something
// is. The numbers are never hidden by a failed check.
func usageDetail(a Account, threshold float64, now time.Time) string {
	detail := "usage not checked yet"
	if a.Usage != nil {
		detail = usageText(Account{Usage: a.Usage}, threshold, now)
		if age := now.Sub(a.Usage.CheckedAt); !a.Usage.CheckedAt.IsZero() && age >= usageAgeShownAfter {
			detail += " · checked " + ageText(age)
		}
	}
	if a.Problem != "" {
		detail += " · " + a.Problem
	}
	return detail
}

func usageText(a Account, threshold float64, now time.Time) string {
	if a.Problem != "" {
		return a.Problem
	}
	if a.Usage == nil {
		return "usage unknown"
	}
	u := *a.Usage
	parts := []string{"5h unknown", "week unknown"}
	switch {
	case u.SessionKnown:
		parts[0] = fmt.Sprintf("5h %.0f%%", u.Session)
		if !u.SessionReset.After(now) {
			parts[0] += " (refresh needed)"
		}
	case u.SessionIdle:
		parts[0] = "5h 0%" // no 5-hour window is running on this account
	}
	if u.WeeklyKnown {
		parts[1] = fmt.Sprintf("week %.0f%%", u.Weekly)
		if !u.WeeklyReset.After(now) {
			parts[1] += " (refresh needed)"
		}
	}
	if u.SessionKnown && u.Session >= threshold && u.SessionReset.After(now) {
		parts[0] += " until " + resetText(u.SessionReset, now)
	}
	// The weekly reset is always shown: it decides how much of the week is left to use.
	switch {
	case !u.WeeklyKnown || !u.WeeklyReset.After(now):
	case u.Weekly >= threshold:
		parts[1] += " until " + resetText(u.WeeklyReset, now)
	default:
		parts[1] += ", resets " + resetText(u.WeeklyReset, now)
	}
	return strings.Join(parts, " · ")
}

func resetText(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return at.Format("15:04")
	case at.Sub(now) < 6*24*time.Hour && now.Sub(at) < 6*24*time.Hour:
		return at.Format("Mon 15:04")
	}
	// A weekday alone would read as today for a reset a week out.
	return at.Format("Mon 2 Jan 15:04")
}

func shorten(text string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

// carriedCount is how many conversations a switch wrote on the destination.
func carriedCount(o *switchOutcome) int {
	if o == nil || o.Carry == nil {
		return 0
	}
	return len(o.Carry.writes)
}

func buildMenu(snap snapshot, now time.Time) menuModel {
	m := menuModel{AddTitle: "Add account…", AddEnabled: !snap.Busy, ShowFix: snap.Blocked, Paused: snap.Paused,
		Tooltip: "Claude Swap Command", IconPercent: -1}
	activeNumber := 0
	for i, a := range snap.Accounts {
		if len(m.Accounts) == menuAccountSlots {
			break
		}
		row := menuAccountRow{ID: a.UUID, Title: accountTitle(i+1, a), Detail: usageDetail(a, snap.Threshold, now), Active: a.UUID == snap.Active}
		switch {
		case row.Active:
			activeNumber = i + 1
			m.Tooltip = row.Title + "\n" + row.Detail
			if a.Usage != nil && a.Usage.SessionKnown {
				m.IconPercent = a.Usage.Session
			}
		case a.Retired:
			row.Title += " · retired"
		default:
			row.Title = "Switch to " + row.Title
			row.Enabled = !snap.Busy
		}
		m.Accounts = append(m.Accounts, row)
	}
	for _, o := range snap.OldSignIns {
		if len(m.Imports) == menuImportSlots {
			break
		}
		m.Imports = append(m.Imports, menuImportRow{Account: o.Account, Org: o.Org, Title: o.label()})
	}
	if snap.Adding {
		m.AddTitle = "Add account… (waiting for you to sign in to Claude)"
	}

	switch {
	case snap.Busy && snap.Status != "":
		m.Header = snap.Status
	case snap.Busy:
		m.Header = "Working…"
	case snap.Status != "":
		m.Header = snap.Status
	case snap.Blocked:
		m.Header = "An earlier switch was interrupted. Choose Fix an interrupted switch…"
	case snap.Adding:
		m.Header = "Sign in to the new account in Claude. It appears here by itself."
	case len(snap.Accounts) == 0:
		m.Header = "Open Claude and sign in. Your account appears here by itself."
	case snap.Active == "":
		m.Header = "Claude is signed out. Pick an account below."
	case activeNumber == 0:
		m.Header = "Claude is signed in to an account that is not saved yet. It appears here shortly."
	default:
		m.Header = "Account " + strconv.Itoa(activeNumber) + " is active"
		if o := snap.LastOutcome; o != nil && o.To == snap.Active && !o.At.IsZero() && now.Sub(o.At) < menuOutcomeFor {
			switch n := carriedCount(o); {
			case o.Carry == nil && o.From != "" && o.From != o.To:
				m.Header += " · conversations were not carried over (see Status…)"
			case n > 0:
				m.Header += " · " + plural(n, "conversation", "conversations") + " carried over"
			}
		}
	}
	m.Header = shorten(m.Header, menuHeaderLimit)
	return m
}
