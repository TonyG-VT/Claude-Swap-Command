package main

// Conversations filed under an old sign-in: a conversation folder on disk
// that belongs to none of the accounts in the menu (a sign-in from before
// this helper, or an account that was never added). Importing copies its
// conversations into the account that is active, once, with the same rules
// and the same backup a switch uses. From then on they follow every switch
// like any other conversation.
//
// A conversation whose transcript is no longer on disk is left where it is:
// it would open empty. Nothing in the old sign-in's folder is ever changed.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

type oldSignIn struct {
	Account, Org string
	Importable   int // conversations that would be copied now
	Present      int // already on the active account
	Missing      int // their transcript is gone; they stay where they are
	skip         map[string]bool
}

func (o oldSignIn) label() string {
	label := plural(o.Importable, "conversation", "conversations") + " to import"
	var notes []string
	if o.Present > 0 {
		notes = append(notes, fmt.Sprintf("%d already here", o.Present))
	}
	if o.Missing == 1 {
		notes = append(notes, "1 without its transcript")
	} else if o.Missing > 1 {
		notes = append(notes, fmt.Sprintf("%d without their transcripts", o.Missing))
	}
	if len(notes) > 0 {
		label += " (" + strings.Join(notes, ", ") + ")"
	}
	return label
}

// readOldSignIn looks at one conversation folder against the account an
// import would copy into (target may be empty when nobody is signed in).
func readOldSignIn(d desktop, claudeDir string, source, target carryNamespace) (oldSignIn, error) {
	found := oldSignIn{Account: source.Account, Org: source.Org, skip: map[string]bool{}}
	dir, err := safeRelative(d.sessionsDir(), filepath.Join(source.Account, source.Org))
	if err != nil {
		return found, coded("CARRY_FOLDER_UNREADABLE", "a conversation folder is not an ordinary folder")
	}
	folder, err := scanCarryFolder(dir)
	if err != nil {
		return found, err
	}
	for name, record := range folder.records {
		if record.Problem != "" || folder.deleted[record.ID] {
			continue
		}
		transcripts, _ := filepath.Glob(filepath.Join(claudeDir, "projects", "*", record.CLI+".jsonl"))
		if !validID(record.CLI) || len(transcripts) == 0 {
			found.Missing++
			found.skip[name] = true
			continue
		}
		if target.valid() {
			if _, err := os.Lstat(filepath.Join(d.sessionsDir(), target.Account, target.Org, name)); err == nil {
				found.Present++
				continue
			}
		}
		found.Importable++
	}
	return found, nil
}

// discoverOldSignIns lists every conversation folder whose account is not in
// the menu and that holds something to import.
func discoverOldSignIns(d desktop, claudeDir string, known map[string]bool, target carryNamespace) []oldSignIn {
	accounts, err := os.ReadDir(d.sessionsDir())
	if err != nil {
		return nil
	}
	var out []oldSignIn
	for _, account := range accounts {
		if !account.IsDir() || !validID(account.Name()) || known[account.Name()] {
			continue
		}
		orgs, err := os.ReadDir(filepath.Join(d.sessionsDir(), account.Name()))
		if err != nil {
			continue
		}
		for _, org := range orgs {
			if !org.IsDir() || !validID(org.Name()) {
				continue
			}
			found, err := readOldSignIn(d, claudeDir, carryNamespace{account.Name(), org.Name()}, target)
			if err != nil || found.Importable == 0 {
				continue
			}
			out = append(out, found)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account+out[i].Org < out[j].Account+out[j].Org })
	return out
}

// activeNamespaceLocked is the conversation folder of the account the helper
// believes is signed in. Callers hold s.mu.
func (s *switcher) activeNamespaceLocked() carryNamespace {
	if a := s.account(s.active); a != nil {
		return carryNamespace{a.UUID, a.Org}
	}
	return carryNamespace{}
}

func (s *switcher) knownAccountsLocked() map[string]bool {
	known := map[string]bool{}
	for _, a := range s.st.Accounts {
		known[a.UUID] = true
	}
	return known
}

// scanOldSignIns refreshes the list the menu offers. It only reads.
func (s *switcher) scanOldSignIns() {
	s.mu.Lock()
	known, target := s.knownAccountsLocked(), s.activeNamespaceLocked()
	s.mu.Unlock()
	found := discoverOldSignIns(s.env.desktop(), s.env.claudeDir(), known, target)
	s.mu.Lock()
	changed := !reflect.DeepEqual(found, s.oldSignIns)
	s.oldSignIns = found
	s.mu.Unlock()
	if changed {
		s.onChange()
	}
}

// importOldSignIn copies one old sign-in's conversations into the account
// that is signed in, inside a backup, with Claude closed, and opens Claude
// again on the same account.
func (s *switcher) importOldSignIn(account, org string) error {
	source := carryNamespace{account, org}
	if !source.valid() {
		return coded("OLD_SIGNIN_NOT_FOUND", "That old sign-in could not be found any more.")
	}
	s.mu.Lock()
	known, adding := s.knownAccountsLocked(), s.st.AddingAccount
	s.mu.Unlock()
	if known[account] {
		return coded("SOURCE_IS_AN_ACCOUNT", "That sign-in is one of the accounts in the menu. Switch to it instead; its conversations come along.")
	}
	if adding {
		return coded("ADDING_ACCOUNT", "Finish signing in to the new account first, then import.")
	}
	d := s.env.desktop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.resolveInterrupted(ctx); err != nil {
		return err
	}
	s.setStatus("Importing conversations from an old sign-in…")
	var plan *carryPlan
	var found oldSignIn
	target, groupsNote := "", ""
	bk, err := s.guardedRestart(ctx, func(active string) error {
		s.mu.Lock()
		var into carryNamespace
		if a := s.account(active); a != nil {
			into = carryNamespace{a.UUID, a.Org}
		}
		s.mu.Unlock()
		if !into.valid() {
			return coded("NO_ACTIVE_ACCOUNT", "Claude is not signed in to one of the accounts in the menu, so there is no account to import into")
		}
		target = into.Account
		var err error
		if found, err = readOldSignIn(d, s.env.claudeDir(), source, into); err != nil {
			return err
		}
		if found.Importable == 0 {
			return coded("NOTHING_TO_IMPORT", "that old sign-in has nothing left to import")
		}
		if plan, err = planCarry(d, source, into, found.skip); err != nil {
			return err
		}
		if _, err := plan.Apply(); err != nil {
			return err
		}
		if _, err := carryProjectGroups(d, source, into, filepath.Join(s.dir, "spaces-seen.json")); err != nil {
			groupsNote = sanitizeError(err)
		}
		return nil
	})
	entry := journalEntry{Kind: "import_old_signin", From: account, To: target}
	if plan != nil && err == nil {
		entry.Carry = plan.summary()
	}
	s.journal.append(entry.withOutcome(err))
	if err != nil {
		return err
	}
	_ = settleBackup(d, bk)
	imported := len(plan.writes)
	s.mu.Lock()
	body := fmt.Sprintf("%s imported into %s.", plural(imported, "conversation", "conversations"), s.accountShortLocked(target))
	s.mu.Unlock()
	switch {
	case found.Missing == 1:
		body += " 1 without its transcript was left where it is."
	case found.Missing > 1:
		body += fmt.Sprintf(" %d without their transcripts were left where they are.", found.Missing)
	}
	if n := len(plan.Skipped); n > 0 {
		body += fmt.Sprintf(" %s could not be copied safely and stayed too.", plural(n, "other", "others"))
	}
	if groupsNote != "" {
		body += " Project groups were not carried over: " + groupsNote + "."
	}
	log.Printf("import from an old sign-in finished at %s", time.Now().Format(time.RFC3339))
	s.tell("Conversations imported", body, "Conversations imported: "+body)
	s.scanOldSignIns()
	return nil
}
