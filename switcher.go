// Modified for Claude Swap Command (2026).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Account is one of your Claude accounts that the helper has seen signed in.
type Account struct {
	Alias         string                `json:"alias,omitempty"`
	Retired       bool                  `json:"retired,omitempty"`
	UUID          string                `json:"uuid"`
	Org           string                `json:"org"`
	Name          string                `json:"name"`
	Email         string                `json:"email"`
	Plan          string                `json:"plan"`
	Usage         *Usage                `json:"usage,omitempty"`
	Problem       string                `json:"problem,omitempty"`
	RefreshHealth *AccountRefreshHealth `json:"refreshHealth,omitempty"`
}

// refreshProfile takes the account's name, email and plan from the profile
// the service returned for it. A profile that names another account or
// another organisation changes nothing, so a saved sign-in is never
// relabelled as somebody else's.
func (a *Account) refreshProfile(p profile) error {
	if a.UUID == "" || p.Account.UUID != a.UUID || p.Organization.UUID == "" || (a.Org != "" && p.Organization.UUID != a.Org) {
		return errors.New("profile identity does not match the saved account")
	}
	a.Name, a.Email, a.Plan = p.Account.DisplayName, p.Account.Email, p.plan()
	return nil
}

// State is what the helper remembers between runs (state.json).
type State struct {
	SchemaVersion int        `json:"schemaVersion,omitempty"`
	Accounts      []*Account `json:"accounts"`
	// UsageChecksPaused is "Pause usage checks" in the menu.
	UsageChecksPaused bool `json:"usageChecksPaused,omitempty"`
	// AddingAccount: Claude was opened on its sign-in screen for a new account.
	AddingAccount bool `json:"addingAccount,omitempty"`
}

// currentStateSchema is the form of state.json this helper writes. It
// leaves alone a settings file written by a newer helper.
const currentStateSchema = 5

const (
	// From this percentage on, the menu says when the limit resets.
	defaultThreshold = 96
	// The usage service limits how often each account may be asked, and Claude
	// asks it too. The active account is checked more often once it is close.
	activeEvery = 2 * time.Minute
	hotEvery    = time.Minute
	hotAt       = 85
	idleEvery   = 10 * time.Minute
	maxBackoff  = 30 * time.Minute
)

type switcher struct {
	dir             string
	env             switchEnv
	notify          func(title, body string)
	onChange        func()
	journal         *switchJournal
	mu              sync.Mutex
	opMu            sync.Mutex // held while a switch or a usage check runs
	busy            atomic.Bool
	cancelPoll      context.CancelFunc
	st              State
	stateUnreadable bool
	active          string // the account Claude is signed in to, "" when nobody is
	status          string
	signInProblem   string
	addingAt        time.Time
	recoveryBlocked bool // an interrupted switch is waiting for a decision
	lastOutcome     *switchOutcome
	lastSelfCheck   *selfCheck
	checkedClaude   string // the Claude version the last self-test looked at
	oldSignIns      []oldSignIn
	// After the usage service refused, an account is not asked again before
	// its wait is over.
	backoff map[string]time.Time
	strikes map[string]int
}

func newSwitcher(dir string) *switcher {
	s := &switcher{dir: dir, onChange: func() {}, backoff: map[string]time.Time{}, strikes: map[string]int{}}
	s.env = liveSwitchEnv(s)
	if data, err := os.ReadFile(s.statePath()); err == nil {
		// The earlier helper kept "Pause usage checks" under another name.
		var earlier struct {
			Guided struct {
				Paused bool `json:"paused"`
			} `json:"guided"`
		}
		if json.Unmarshal(data, &s.st) != nil {
			s.stateUnreadable = true
			s.status = "The helper's settings file cannot be read, so it is not changing anything. Put back state.json from a backup, or remove it to start over."
		} else if json.Unmarshal(data, &earlier) == nil && earlier.Guided.Paused {
			s.st.UsageChecksPaused = true
		}
	}
	if s.st.SchemaVersion > currentStateSchema {
		s.stateUnreadable = true
		s.status = "The helper's settings were written by a newer version, so this one is not changing anything. Install the newer version."
	}
	s.st.SchemaVersion = currentStateSchema
	s.restoreRefreshHealthLocked(time.Now())
	s.journal = newSwitchJournal(filepath.Join(dir, "journal"))
	if pending, err := pendingBackups(s.app(), s.backupsDir()); err != nil || len(pending) > 0 {
		// The menu's first line says what to do about it (menu.go).
		s.recoveryBlocked = true
	}
	return s
}

func (s *switcher) statePath() string             { return filepath.Join(s.dir, "state.json") }
func (s *switcher) profileDir(uuid string) string { return filepath.Join(s.dir, "accounts", uuid) }
func (s *switcher) backupsDir() string            { return filepath.Join(s.dir, "backups") }

// app looks the data folder up on every use, because Claude can move it in an
// update while the switcher keeps running.
func (s *switcher) app() desktop { return s.env.desktop() }

// saveState writes state.json. Callers hold s.mu.
func (s *switcher) saveState() {
	if s.stateUnreadable {
		return
	}
	data, _ := json.MarshalIndent(s.st, "", "  ")
	if err := writeFileAtomic(s.statePath(), data); err != nil {
		log.Printf("save state: %v", err)
	}
}

func (s *switcher) setStatus(msg string) {
	s.mu.Lock()
	s.status = msg
	s.mu.Unlock()
	if msg != "" {
		log.Print(msg)
	}
	s.onChange()
}

func (s *switcher) account(uuid string) *Account {
	for _, a := range s.st.Accounts {
		if a.UUID == uuid {
			return a
		}
	}
	return nil
}

// tokenFor returns an access token for an account: from the app's live config
// when it is signed in, otherwise from its saved profile.
func (s *switcher) tokenFor(uuid, active string) (string, error) {
	if !idPattern.MatchString(uuid) {
		return "", errors.New("invalid account identity")
	}
	path := filepath.Join(s.profileDir(uuid), "config.json")
	if uuid == active {
		path = s.app().configPath()
	}
	cache, err := tokenCacheOf(path)
	if err != nil {
		return "", err
	}
	return accessToken(cache, uuid, s.env.decrypt)
}

// noticeSignIn reads, without the network, who is signed in to Claude right
// now. An unreadable sign-in keeps the last known answer and is reported.
func (s *switcher) noticeSignIn() (active string, ok bool) {
	state, err := readSignIn(s.app().configPath(), s.env.decrypt)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err != nil:
		problem := fmt.Sprintf("Claude's sign-in could not be read [%s]: %s", errorCode(err), sanitizeError(err))
		if s.signInProblem != problem {
			s.signInProblem = problem
			log.Print(problem)
		}
		return s.active, false
	case state.Multiple():
		s.signInProblem = "Claude has more than one account signed in at once [MULTIPLE_ACCOUNTS_SIGNED_IN]. Switching is paused."
		s.active = ""
		return "", false
	}
	s.signInProblem = ""
	s.active = state.Account
	return state.Account, true
}

// check fetches the usage that is due and saves it. It never restarts Claude,
// so -status can use it. A switch that starts meanwhile cancels it: a usage
// check is never a reason to wait.
func (s *switcher) check(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.busy.Load() || s.stateUnreadable {
		s.mu.Unlock()
		return
	}
	s.cancelPoll = cancel
	paused := s.st.UsageChecksPaused
	s.mu.Unlock()

	active, readable := s.noticeSignIn()
	if !readable || paused {
		s.onChange()
		if !readable {
			return
		}
	}

	type job struct {
		uuid string
		new  bool
	}
	s.mu.Lock()
	now := time.Now()
	var jobs []job
	if active != "" && s.account(active) == nil && now.After(s.backoff[active]) {
		jobs = append(jobs, job{active, true})
	}
	for _, a := range s.st.Accounts {
		if a.Retired || paused {
			continue
		}
		every := idleEvery
		if a.UUID == active {
			every = activeEvery
			if a.Usage != nil && a.Usage.peak() >= hotAt {
				every = hotEvery
			}
		}
		due := a.Usage == nil || now.Sub(a.Usage.CheckedAt) >= every-5*time.Second
		if due && now.After(s.backoff[a.UUID]) {
			jobs = append(jobs, job{a.UUID, false})
		}
	}
	s.mu.Unlock()

	addedNow := ""
	for _, j := range jobs {
		token, err := s.tokenFor(j.uuid, active)
		if errors.Is(err, errSignedOut) && !j.new {
			// The saved token has run out. That says nothing about the account:
			// Claude renews it once the account is switched to. The last numbers
			// stay, shown with their age.
			s.mu.Lock()
			if a := s.account(j.uuid); a != nil && a.RefreshHealth == nil {
				a.Problem = ""
			}
			s.backoff[j.uuid] = time.Now().Add(idleEvery)
			s.mu.Unlock()
			continue
		}
		var p profile
		var u *Usage
		profileVerified := false
		if err == nil {
			p, err = fetchProfile(ctx, token)
		}
		if err == nil && (!idPattern.MatchString(p.Account.UUID) || !idPattern.MatchString(p.Organization.UUID) || p.Account.UUID != j.uuid) {
			err = errors.New("profile identity mismatch")
		}
		if err == nil {
			profileVerified = true
			u, err = fetchUsage(ctx, token)
		}
		if ctx.Err() != nil {
			// Cut short by a switch or by the helper closing: no evidence either way.
			return
		}

		s.mu.Lock()
		a := s.account(j.uuid)
		if j.new && a == nil && profileVerified {
			a = &Account{UUID: j.uuid, Org: p.Organization.UUID, Name: p.Account.DisplayName, Email: p.Account.Email, Plan: p.plan()}
			s.st.Accounts = append(s.st.Accounts, a)
			addedNow = j.uuid
			log.Printf("added %s", s.accountShortLocked(j.uuid))
		}
		identityInvalid := false
		if err == nil {
			err = s.applyVerifiedRefreshLocked(a, p, u)
			identityInvalid = err != nil
		}
		if err != nil {
			s.recordRefreshFailureLocked(j.uuid, err, time.Now())
			if a != nil && identityInvalid {
				a.Problem = "this sign-in's account details did not match; sign in to it again"
			}
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	finished := ""
	if s.st.AddingAccount && active != "" && s.account(active) != nil {
		// The sign-in the person was asked for has arrived.
		s.st.AddingAccount = false
		s.addingAt = time.Time{}
		s.status = ""
		finished = active
	}
	s.saveState()
	var title, short string
	if finished != "" {
		short = s.accountShortLocked(finished)
		for i, a := range s.st.Accounts {
			if a.UUID == finished {
				title = accountTitle(i+1, *a)
			}
		}
	}
	s.mu.Unlock()
	switch {
	case finished != "" && finished == addedNow:
		s.journal.addAccount("signed_in", "", finished, nil)
		s.tell("Account added", title+" is signed in and saved in the menu.", short+" was added")
	case finished != "":
		s.journal.addAccount("signed_in", "", finished, nil)
		s.tell("Signed in", title+" was already in the menu, so nothing was added.", short+" signed in again; nothing was added")
	}
	s.onChange()
}

// tell shows a short message outside the menu (a notification). logged is
// what goes to the log for it: accounts by number, never a name or an email.
func (s *switcher) tell(title, body, logged string) {
	log.Print(logged)
	if s.notify != nil {
		s.notify(title, body)
	}
}

// watchClaude runs the self-test when the helper starts and whenever Claude
// has changed version, so a Claude update that breaks switching is heard
// about when it happens and not when a switch is needed.
func (s *switcher) watchClaude(ctx context.Context) {
	claude := s.env.claudeVersion()
	s.mu.Lock()
	same := s.lastSelfCheck != nil && s.checkedClaude == claude
	busy := s.busy.Load()
	s.mu.Unlock()
	if same || busy {
		return
	}
	check := s.env.selfCheck(ctx)
	s.mu.Lock()
	s.lastSelfCheck, s.checkedClaude = &check, claude
	s.mu.Unlock()
	blocker := check.blocker()
	s.journal.append(journalEntry{Kind: "self_check", Claude: check.ClaudeVersion, Stage: map[bool]string{true: "can_carry", false: "cannot_carry"}[check.CanCarry]}.withOutcome(blocker))
	log.Print(check.summary())
	if blocker != nil {
		text := fmt.Sprintf("%s [%s]", sanitizeError(blocker), errorCode(blocker))
		s.tell("Switching is paused", text+" Open Status… in the menu for details.", "Switching is paused: "+text)
	}
	s.onChange()
}

// run does one app restart at a time and reports the outcome in the menu.
func (s *switcher) run(what string, f func() error) error {
	if s.stateUnreadable {
		err := coded("STATE_UNREADABLE", "The helper's saved settings could not be read, so it is not changing anything. Open Status… for details.")
		s.setStatus(sanitizeError(err))
		return err
	}
	if !s.busy.CompareAndSwap(false, true) {
		return coded("BUSY", "Something else is still running. Wait for it to finish, then try again.")
	}
	// A usage check in flight is cancelled instead of waited for.
	s.mu.Lock()
	if s.cancelPoll != nil {
		s.cancelPoll()
	}
	s.mu.Unlock()
	s.opMu.Lock()
	err := f()
	s.opMu.Unlock()
	s.busy.Store(false)
	if err != nil {
		// The status line is also the switcher.log line: the operation, the
		// code and the described cause, never raw library text.
		s.setStatus(fmt.Sprintf("%s failed [%s]: %s", what, errorCode(err), sanitizeError(err)))
		return err
	}
	s.mu.Lock()
	if s.addingAt.IsZero() {
		s.status = ""
	}
	s.mu.Unlock()
	s.onChange()
	s.check(context.Background())
	return nil
}

// snapshot is a copy of what the menu shows.
type snapshot struct {
	Accounts    []Account
	Active      string
	Threshold   float64
	Status      string
	Busy        bool
	Paused      bool
	Adding      bool
	Blocked     bool // an interrupted switch is waiting for a decision
	LastOutcome *switchOutcome
	OldSignIns  []oldSignIn
}

func (s *switcher) snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := snapshot{Active: s.active, Threshold: defaultThreshold, Status: s.status, Busy: s.busy.Load(),
		Paused: s.st.UsageChecksPaused, Adding: s.st.AddingAccount, Blocked: s.recoveryBlocked && !s.stateUnreadable, LastOutcome: s.lastOutcome,
		OldSignIns: append([]oldSignIn(nil), s.oldSignIns...)}
	if snap.Status == "" && !snap.Busy {
		snap.Status = s.signInProblem
	}
	for _, a := range s.st.Accounts {
		snap.Accounts = append(snap.Accounts, *a)
	}
	return snap
}

func (s *switcher) adding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.AddingAccount
}

// setUsageChecksPaused is "Pause usage checks" in the menu. It stops only the
// questions to the usage service; noticing who is signed in, and switching,
// go on. It does not wait for a switch or a check that is running.
func (s *switcher) setUsageChecksPaused(on bool) error {
	s.mu.Lock()
	if s.stateUnreadable {
		s.mu.Unlock()
		return coded("STATE_UNREADABLE", "The helper's saved settings could not be read, so the setting was not changed.")
	}
	previous := s.st.UsageChecksPaused
	s.st.UsageChecksPaused = on
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err == nil {
		err = writeFileAtomic(s.statePath(), data)
	}
	if err != nil {
		s.st.UsageChecksPaused = previous
		err = codedf("SETTING_NOT_SAVED", "the setting could not be saved (%w)", err)
	}
	s.mu.Unlock()
	s.onChange()
	return err
}
