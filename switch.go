package main

// A switch made from the menu, start to finish.
//
//  1. Check, without the network, that the saved sign-in for the chosen
//     account is complete and really belongs to it.
//  2. Self-test (selfcheck.go). Nothing has been touched yet.
//  3. Ask Claude to quit the normal way and wait until it is truly closed
//     (closed.go). Self-test again: Claude's updater may have run.
//  4. Inside a backup: carry the conversations, save the current sign-in,
//     put the chosen account's sign-in in place (profiles.go), and check what
//     is now on disk.
//  5. Start Claude and watch what it does with the sign-in.
//
// A failure in step 4 puts everything back and reopens Claude. Once Claude is
// running again nothing can be put back underneath it, so from then on the
// switch is only reported, and "going back" is another switch.
//
// The usage service is never asked: eight real switches were once refused
// only because it was rate-limiting.

import (
	"context"
	"log"
	"path/filepath"
	"strconv"
	"time"
)

// switchEnv is the outside world as a switch sees it. Tests supply their own.
type switchEnv struct {
	desktop   func() desktop
	decrypt   cacheDecrypter
	selfCheck func(context.Context) selfCheck
	// close asks Claude to quit and returns nil once it is fully closed.
	close func(ctx context.Context, onWait func(waitingFor []string)) error
	// closed is one look: is Claude closed right now?
	closed func(context.Context) bool
	start  func() error
	// identify asks the service which account the live sign-in belongs to.
	identify func(ctx context.Context, account string) (string, error)
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	// claudeVersion is the installed Claude's version, "" when unknown.
	claudeVersion func() string
	// claudeDir is where Claude Code keeps its transcripts (~/.claude).
	claudeDir func() string

	verifyPoll, verifyLimit, signedOutGrace time.Duration
	// verifySettle is how long Claude gets after it starts before the
	// service's word about the sign-in counts as confirmation.
	verifySettle time.Duration
}

// switchVerdict is what Claude was seen to do with the sign-in after launch.
type switchVerdict string

const (
	verdictConfirmed        switchVerdict = "confirmed"         // the chosen account is signed in
	verdictUnconfirmed      switchVerdict = "unconfirmed"       // it looks signed in, but nothing proved it yet
	verdictNeedsSignIn      switchVerdict = "needs_sign_in"     // Claude dropped the saved sign-in
	verdictDifferentAccount switchVerdict = "different_account" // somebody else is signed in
)

// switchOutcome is what the last switch did, for the menu and the journal.
type switchOutcome struct {
	From, To   string
	Verdict    switchVerdict
	Carry      *carryPlan // nil when conversations were not carried
	CarryNote  string     // why not, in plain words
	Groups     int        // project groups carried
	GroupsNote string
	At         time.Time
}

func (o *switchOutcome) journalCarry() *journalCarry {
	switch {
	case o == nil:
		return nil
	case o.Carry != nil:
		return o.Carry.summary()
	case o.From != "":
		return &journalCarry{Outcome: "none", Reason: o.CarryNote}
	}
	return nil
}

// accountShortLocked names an account in a sentence: "Account 2". Callers hold s.mu.
func (s *switcher) accountShortLocked(uuid string) string {
	for i, a := range s.st.Accounts {
		if a.UUID == uuid {
			return "Account " + strconv.Itoa(i+1)
		}
	}
	return "that account"
}

func (s *switcher) waitingOnClaude(waitingFor []string) {
	text := "Waiting for Claude to close…"
	for _, kind := range waitingFor {
		switch kind {
		case "Claude":
			text = "Waiting for Claude to close… (if Claude is asking a question, answer it there)"
		case "a Claude Code task":
			if len(waitingFor) == 1 {
				text = "Waiting for a Claude Code task to finish closing…"
			}
		case "Claude's updater":
			if len(waitingFor) == 1 {
				text = "Waiting for Claude's updater to finish…"
			}
		}
	}
	s.mu.Lock()
	changed := s.status != text
	s.status = text
	s.mu.Unlock()
	if changed {
		s.onChange()
	}
}

// guardedRestart closes Claude, runs change inside a backup and starts Claude
// again. change receives the account that is signed in ("" when nobody is).
// The returned backup is sealed but still open: the caller closes it with
// settleBackup once it has looked at what Claude did.
func (s *switcher) guardedRestart(ctx context.Context, change func(active string) error) (string, error) {
	d := s.env.desktop()
	bk, err := runDesktopTransaction(transactionOps{
		Prepare: func() error {
			check := s.env.selfCheck(ctx)
			s.noteSelfCheck(check)
			return check.blocker()
		},
		Quit: func() error {
			if err := s.env.close(ctx, s.waitingOnClaude); err != nil {
				return err
			}
			// Claude's updater may have installed a new version while it was closed.
			check := s.env.selfCheck(ctx)
			s.noteSelfCheck(check)
			if err := check.blocker(); err != nil {
				_ = s.env.start()
				return err
			}
			return nil
		},
		Closed: func() bool { return s.env.closed(ctx) },
		Backup: func() (string, error) {
			return backup(d, s.backupsDir(), filepath.Join(s.dir, "spaces-seen.json"))
		},
		Active: func() (string, error) {
			state, err := readSignIn(d.configPath(), s.env.decrypt)
			if err != nil {
				return "", err
			}
			if state.Multiple() {
				return "", coded("MULTIPLE_ACCOUNTS_SIGNED_IN", "Claude has more than one account signed in at once, which this helper cannot swap safely")
			}
			return state.Account, nil
		},
		Seal:    func(p string) error { return sealBackup(d, p) },
		Restore: func(p string) error { return restoreBackup(d, p) },
		Start:   s.env.start,
	}, change)
	if err != nil {
		switch errorCode(err) {
		case "CLAUDE_REOPENED", "LAUNCH_UNCERTAIN":
			// Claude is running again: nothing can be rolled back underneath it,
			// and an open transaction would refuse every later switch.
			if bk != "" {
				_ = settleBackup(d, bk)
			}
		case "RECOVERY_NEEDED":
			// The put-back is still owed. The message says to use the fix, so
			// the menu has to offer it now, not after the next failed attempt.
			s.mu.Lock()
			s.recoveryBlocked = true
			s.mu.Unlock()
			s.onChange()
		}
	}
	return bk, err
}

func (s *switcher) noteSelfCheck(check selfCheck) {
	s.mu.Lock()
	s.lastSelfCheck = &check
	s.mu.Unlock()
}

// carryConversations copies the source account's conversations and project
// groups to the target. A folder that cannot be planned is noted and the
// account still switches; a copy that fails midway is an error, which makes
// the surrounding backup put everything back.
func (s *switcher) carryConversations(d desktop, source, target Account, outcome *switchOutcome) error {
	src, dst := carryNamespace{source.UUID, source.Org}, carryNamespace{target.UUID, target.Org}
	plan, err := planCarry(d, src, dst, nil)
	if err != nil {
		outcome.CarryNote = sanitizeError(err)
		return nil
	}
	if _, err := plan.Apply(); err != nil {
		return err
	}
	outcome.Carry = plan
	if n, err := carryProjectGroups(d, src, dst, filepath.Join(s.dir, "spaces-seen.json")); err != nil {
		outcome.GroupsNote = sanitizeError(err)
	} else {
		outcome.Groups = n
	}
	return nil
}

// saveSignIn saves the sign-in Claude holds right now as account's saved
// sign-in. Claude's files are only worth copying while Claude is closed, and
// the copy replaces the stored one only if it is complete and really is that
// account's.
func (s *switcher) saveSignIn(ctx context.Context, d desktop, account string) error {
	if !s.env.closed(ctx) {
		return coded("CLAUDE_REOPENED", "Claude reopened before its sign-in was saved")
	}
	err := saveProfileKeepingPrevious(d, s.profileDir(account), func(staging string) error {
		saved, err := checkSavedSignIn(staging, account, s.env.decrypt)
		if err == nil && saved.Empty {
			err = coded("SAVED_SIGNIN_MISMATCH", "the copy of the sign-in that was just made holds no sign-in")
		}
		return err
	})
	if err != nil {
		return codedf("SAVE_SIGNIN_FAILED", "the current sign-in could not be saved (%w)", err)
	}
	return nil
}

// switchTo signs Claude in as the account uuid.
func (s *switcher) switchTo(uuid string) error {
	s.mu.Lock()
	var target Account
	found := s.account(uuid)
	if found != nil {
		target = *found
	}
	targetName := s.accountShortLocked(uuid)
	s.mu.Unlock()
	if found == nil {
		return coded("UNKNOWN_ACCOUNT", "That account is not set up in this helper.")
	}
	if target.Retired {
		return coded("ACCOUNT_RETIRED", "That account is retired in this helper.")
	}
	d := s.env.desktop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if state, err := readSignIn(d.configPath(), s.env.decrypt); err == nil && state.Account == uuid {
		s.mu.Lock()
		s.active = uuid
		s.mu.Unlock()
		return nil
	}
	if _, err := checkSavedSignIn(s.profileDir(uuid), uuid, s.env.decrypt); err != nil {
		return err
	}
	if err := s.resolveInterrupted(ctx); err != nil {
		return err
	}

	outcome := &switchOutcome{To: uuid}
	var restored signInState
	s.setStatus("Switching to " + targetName + "…")
	bk, err := s.guardedRestart(ctx, func(active string) error {
		outcome.From = active
		if active == uuid {
			return nil
		}
		if active != "" {
			s.mu.Lock()
			var source Account
			saved := s.account(active)
			if saved != nil {
				source = *saved
			}
			s.mu.Unlock()
			if saved == nil {
				return coded("ACCOUNT_NOT_SAVED", "Claude is signed in to an account this helper has not saved yet. Open Claude, wait until that account appears in the helper's menu, then switch")
			}
			if err := s.carryConversations(d, source, target, outcome); err != nil {
				return err
			}
			if err := s.saveSignIn(ctx, d, active); err != nil {
				return err
			}
		}
		if err := restoreProfileSafely(d, s.profileDir(uuid), func() bool { return s.env.closed(ctx) }); err != nil {
			if errorCode(err) == "UNCLASSIFIED" {
				return codedf("RESTORE_SIGNIN_FAILED", "the saved sign-in could not be put in place (%w)", err)
			}
			return err
		}
		state, err := readSignIn(d.configPath(), s.env.decrypt)
		if err != nil {
			return err
		}
		if state.Multiple() || (state.Account != "" && state.Account != uuid) {
			return coded("SAVED_SIGNIN_MISMATCH", "the saved sign-in that was put in place belongs to a different account")
		}
		restored = state
		return nil
	})
	from := outcome.From
	if err != nil {
		s.journal.manualSwitch(from, uuid, nil, "", err)
		return err
	}

	// Claude is running again. Watch what it does with the sign-in, then
	// close the backup: it can no longer be rolled back.
	outcome.Verdict = verdictConfirmed
	if from != uuid {
		outcome.Verdict = s.verifyAfterLaunch(ctx, uuid, restored)
	}
	outcome.At = s.env.now()
	if err := settleBackup(d, bk); err == nil {
		if removed, err := pruneBackups(d, s.backupsDir(), time.Now()); err != nil {
			log.Printf("old backups could not all be removed: %s", describeCause(err))
		} else if removed > 0 {
			log.Printf("removed %d old backups", removed)
		}
	}

	s.mu.Lock()
	fromName := s.accountShortLocked(from)
	var verdictErr error
	switch outcome.Verdict {
	case verdictNeedsSignIn:
		if from != "" {
			verdictErr = codedf("SIGNIN_NOT_ACCEPTED", "Claude did not accept the saved sign-in for %s and is showing its sign-in screen. Sign in to that account there, or switch back to %s.", targetName, fromName)
		} else {
			verdictErr = codedf("SIGNIN_NOT_ACCEPTED", "Claude did not accept the saved sign-in for %s and is showing its sign-in screen. Sign in to that account there.", targetName)
		}
		s.active = ""
	case verdictDifferentAccount:
		verdictErr = codedf("DIFFERENT_ACCOUNT", "After the switch Claude is signed in to a different account than %s. Look at Claude, then pick the account again.", targetName)
		s.active = ""
	default:
		s.active = uuid
		s.st.AddingAccount = false
		s.addingAt = time.Time{}
	}
	s.lastOutcome = outcome
	s.saveState()
	s.mu.Unlock()
	s.journal.manualSwitch(from, uuid, outcome.journalCarry(), string(outcome.Verdict), verdictErr)
	return verdictErr
}

// verifyAfterLaunch watches what Claude does with a sign-in that was just put
// in place. Claude keeps its settings entries when it rejects a sign-in and
// only empties them, so the entries themselves are read (signin.go).
// Proof that the account is in: Claude renewed its tokens, or, once Claude
// has had time to act on the sign-in, the service names the account. Proof that it is not: the entries are gone, or name
// somebody else. Without either, the result is "unconfirmed", never success.
func (s *switcher) verifyAfterLaunch(ctx context.Context, target string, restored signInState) switchVerdict {
	d := s.env.desktop()
	start := s.env.now()
	grace := s.env.signedOutGrace
	if restored.SignedOut() {
		// The saved copy held no tokens: Claude needs time to make new ones
		// from the web session before "signed out" means anything.
		grace = s.env.verifyLimit / 2
	}
	var signedOutSince, lastAsked time.Time
	looksRight := false
	for {
		now := s.env.now()
		state, err := readSignIn(d.configPath(), s.env.decrypt)
		switch {
		case err != nil:
			// Claude may be rewriting its settings this instant; look again.
		case state.Multiple() || (state.Account != "" && state.Account != target):
			return verdictDifferentAccount
		case state.Account == target:
			looksRight, signedOutSince = true, time.Time{}
			// Claude writing fresh tokens is Claude's own word. Claude throwing
			// one of the entries away also changes the fingerprint, and is not.
			if state.Fingerprint != restored.Fingerprint && state.Entries >= restored.Entries {
				return verdictConfirmed
			}
			// The service knowing the token does not mean Claude kept it:
			// Claude drops a sign-in it does not like within its first seconds.
			// And it only counts while Claude is actually running.
			if now.Sub(start) >= s.env.verifySettle && (lastAsked.IsZero() || now.Sub(lastAsked) >= 10*time.Second) && !s.env.closed(ctx) {
				lastAsked = now
				if who, err := s.env.identify(ctx, target); err == nil {
					if who == target {
						return verdictConfirmed
					}
					return verdictDifferentAccount
				}
			}
		default:
			looksRight = false
			if signedOutSince.IsZero() {
				signedOutSince = now
			}
			if now.Sub(signedOutSince) >= grace {
				return verdictNeedsSignIn
			}
		}
		if now.Sub(start) >= s.env.verifyLimit {
			break
		}
		if err := s.env.sleep(ctx, s.env.verifyPoll); err != nil {
			break
		}
	}
	if looksRight {
		return verdictUnconfirmed
	}
	return verdictNeedsSignIn
}

// resolveInterrupted deals with a switch that was cut short (the helper was
// stopped in the middle). While Claude is running nothing can be put back
// underneath it, so the leftover is closed as it is. With Claude closed the
// person decides, through "Fix an interrupted switch…".
func (s *switcher) resolveInterrupted(ctx context.Context) error {
	d := s.env.desktop()
	pending, err := pendingBackups(d, s.backupsDir())
	if err != nil {
		return codedf("INTERRUPTED_SWITCH", "An earlier switch was interrupted and its backup could not be read (%w). Choose Fix an interrupted switch… in the menu.", err)
	}
	if len(pending) == 0 {
		s.mu.Lock()
		s.recoveryBlocked = false
		s.mu.Unlock()
		return nil
	}
	if s.env.closed(ctx) {
		s.mu.Lock()
		s.recoveryBlocked = true
		s.mu.Unlock()
		return coded("INTERRUPTED_SWITCH", "An earlier switch was interrupted. Choose Fix an interrupted switch… in the menu.")
	}
	for _, dir := range pending {
		if err := settleBackup(d, dir); err != nil {
			return codedf("INTERRUPTED_SWITCH", "An earlier switch was interrupted and could not be closed (%w). Choose Fix an interrupted switch… in the menu.", err)
		}
	}
	s.journal.note("recover", "closed "+strconv.Itoa(len(pending))+" interrupted switch(es) while Claude was running")
	s.mu.Lock()
	s.recoveryBlocked = false
	s.mu.Unlock()
	return nil
}

// fixInterruptedSwitch is the menu's "Fix an interrupted switch…". putBack
// restores the files the interrupted switch had changed (Claude is closed
// first); otherwise things stay as they are. Either way the leftover is
// closed and Claude is opened.
func (s *switcher) fixInterruptedSwitch(ctx context.Context, putBack bool) error {
	d := s.env.desktop()
	pending, err := pendingBackups(d, s.backupsDir())
	if err != nil {
		// The marker names a backup that can no longer be read.
		if putBack {
			return coded("BACKUP_UNREADABLE", "The interrupted switch's backup can no longer be read, so nothing can be put back from it. Choose Fix an interrupted switch… again and pick Keep as it is.")
		}
		if err := setAsideUnreadableBackup(d, s.backupsDir()); err != nil {
			return codedf("INTERRUPTED_SWITCH", "the interrupted switch could not be closed (%w)", err)
		}
		pending = nil
	}
	var problem error
	if putBack && len(pending) > 0 {
		if err := s.env.close(ctx, s.waitingOnClaude); err != nil {
			return err
		}
		for i := len(pending) - 1; i >= 0; i-- {
			err := restoreBackup(d, pending[i])
			if err == nil {
				continue
			}
			if backupPhase(d, pending[i]) == "restoring" {
				// It stopped partway. It is still owed, and Claude stays closed:
				// what is on disk now is nobody's sign-in.
				s.mu.Lock()
				s.recoveryBlocked = true
				s.mu.Unlock()
				incomplete := codedf("PUT_BACK_INCOMPLETE", "Putting things back stopped partway (%w). Claude was left closed. Check that the disk has free space, then choose Fix an interrupted switch… again. Picking Keep as it is leaves things as they are now.", err)
				s.journal.append(journalEntry{Kind: "recover", Stage: "put_back"}.withOutcome(incomplete))
				s.onChange()
				return incomplete
			}
			// Claude has rewritten those files since: they stay as they are.
			problem = coded("PUT_BACK_NOT_POSSIBLE", "Claude has changed those files since the interrupted switch, so they were left as they are.")
			_ = settleBackup(d, pending[i])
		}
	} else {
		for _, dir := range pending {
			if err := settleBackup(d, dir); err != nil && problem == nil {
				problem = codedf("INTERRUPTED_SWITCH", "the interrupted switch could not be closed (%w)", err)
			}
		}
	}
	s.mu.Lock()
	s.recoveryBlocked = false
	s.mu.Unlock()
	s.journal.append(journalEntry{Kind: "recover", Stage: map[bool]string{true: "put_back", false: "keep"}[putBack]}.withOutcome(problem))
	if s.env.closed(ctx) {
		_ = s.env.start()
	}
	return problem
}

// addAccount saves the current sign-in, clears it locally and opens Claude on
// its own sign-in screen. Claude's Sign out is never used: that would revoke
// the sign-in that was just saved.
func (s *switcher) addAccount() error {
	d := s.env.desktop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.resolveInterrupted(ctx); err != nil {
		return err
	}
	s.setStatus("Saving the current sign-in…")
	from := ""
	bk, err := s.guardedRestart(ctx, func(active string) error {
		from = active
		if active != "" {
			s.mu.Lock()
			known := s.account(active) != nil
			s.mu.Unlock()
			if !known {
				return coded("ACCOUNT_NOT_SAVED", "Claude is signed in to an account this helper has not saved yet. Wait until that account appears in the helper's menu, then add the next one")
			}
			if err := s.saveSignIn(ctx, d, active); err != nil {
				return err
			}
		}
		if err := clearSignIn(d); err != nil {
			return codedf("CLEAR_SIGNIN_FAILED", "Claude's sign-in could not be cleared (%w)", err)
		}
		return nil
	})
	s.journal.addAccount("reopen_for_sign_in", from, "", err)
	if err != nil {
		return err
	}
	_ = settleBackup(d, bk)
	s.mu.Lock()
	s.addingAt = s.env.now()
	s.st.AddingAccount = true
	s.active = ""
	s.saveState()
	s.mu.Unlock()
	return nil
}
