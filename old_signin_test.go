package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldSignInFixture adds, to the switch fixture, a conversation folder that
// belongs to no account in the menu: three conversations, one of which is
// already on account A and one whose transcript is gone.
func oldSignInFixture(t *testing.T) (*switchFixture, carryNamespace, *[]string) {
	t.Helper()
	f := newSwitchFixture(t)
	old := carryNamespace{carryID(0xE1), carryID(0xE2)}
	f.record(old, 21, map[string]any{"title": "Only in the old sign-in"})
	f.record(old, 22, nil)
	f.record(f.a, 22, nil)
	f.record(old, 23, map[string]any{"title": "Its transcript is gone"})
	dotClaude := filepath.Join(f.root, "dot-claude")
	for _, n := range []int{21, 22} {
		write(t, filepath.Join(dotClaude, "projects", "-work-project", carryID(0xC000+n)+".jsonl"), "{}\n")
	}
	f.s.env.claudeDir = func() string { return dotClaude }
	told := &[]string{}
	f.s.notify = func(title, body string) { *told = append(*told, title+": "+body) }
	return f, old, told
}

func TestOldSignInIsFoundAndImportedIntoTheActiveAccount(t *testing.T) {
	f, old, told := oldSignInFixture(t)
	f.s.scanOldSignIns()
	found := f.s.snapshot().OldSignIns
	if len(found) != 1 || found[0].Account != old.Account || found[0].Org != old.Org {
		t.Fatalf("only the folder that belongs to no account in the menu is listed: %+v", found)
	}
	if o := found[0]; o.Importable != 1 || o.Present != 1 || o.Missing != 1 {
		t.Fatalf("counts: %+v", o)
	}
	if got := found[0].label(); got != "1 conversation to import (1 already here, 1 without its transcript)" {
		t.Fatalf("label = %q", got)
	}
	oldBefore := map[int]string{}
	for _, n := range []int{21, 22, 23} {
		oldBefore[n] = read(t, f.recordPath(old, n))
	}

	if err := f.s.importOldSignIn(old.Account, old.Org); err != nil {
		t.Fatalf("import: %v", err)
	}
	if f.closes != 1 || f.starts != 1 || !f.running || len(f.pending()) != 0 {
		t.Fatalf("Claude closes once and reopens, and nothing is left pending: closes=%d starts=%d", f.closes, f.starts)
	}
	if f.live().Account != f.a.Account || read(t, f.d.cookieFiles()[0]) != "cookies-a" {
		t.Fatal("the sign-in is not touched by an import")
	}
	if read(t, f.recordPath(f.a, 21)) != oldBefore[21] {
		t.Fatal("the conversation is copied exactly into the active account")
	}
	if _, err := os.Stat(f.recordPath(f.a, 23)); !os.IsNotExist(err) {
		t.Fatal("a conversation whose transcript is gone is left where it is")
	}
	for n, before := range oldBefore {
		if read(t, f.recordPath(old, n)) != before {
			t.Fatalf("nothing in the old sign-in is changed (conversation %d)", n)
		}
	}
	if len(*told) != 1 || !strings.Contains((*told)[0], "1 conversation imported into Account 1") || !strings.Contains((*told)[0], "1 without its transcript was left") {
		t.Fatalf("the person is told what happened: %v", *told)
	}
	entries := f.journal()
	if last := entries[len(entries)-1]; last.Kind != "import_old_signin" || last.Result != "ok" || last.From != old.Account || last.To != f.a.Account || last.Carry == nil || last.Carry.New != 1 || last.Carry.Identical != 1 {
		t.Fatalf("journal: %+v %+v", last, last.Carry)
	}
	// With nothing left to import, the menu no longer offers it.
	if left := f.s.snapshot().OldSignIns; len(left) != 0 {
		t.Fatalf("nothing is left to import: %+v", left)
	}
}

func TestOldSignInImportRefusesWhenItMakesNoSense(t *testing.T) {
	f, old, _ := oldSignInFixture(t)
	if err := f.s.importOldSignIn(f.b.Account, f.b.Org); errorCode(err) != "SOURCE_IS_AN_ACCOUNT" {
		t.Fatalf("one of the menu's own accounts is switched to, not imported: %v", err)
	}
	if err := f.s.importOldSignIn("not-an-id", old.Org); errorCode(err) != "OLD_SIGNIN_NOT_FOUND" {
		t.Fatalf("a folder that cannot be identified: %v", err)
	}
	if f.closes != 0 {
		t.Fatal("Claude is not closed for an import that is refused")
	}
	// Nobody is signed in: there is no account to import into.
	f.signIn(f.d, f.a, "")
	before := read(t, f.recordPath(old, 21))
	err := f.s.importOldSignIn(old.Account, old.Org)
	if errorCode(err) != "NO_ACTIVE_ACCOUNT" || !strings.Contains(err.Error(), "everything was put back") {
		t.Fatalf("want NO_ACTIVE_ACCOUNT, got %v", err)
	}
	if read(t, f.recordPath(old, 21)) != before || len(f.pending()) != 0 || !f.running {
		t.Fatal("nothing changes and Claude is reopened")
	}
}

func TestOldSignInScanIgnoresFoldersWithNothingToImport(t *testing.T) {
	f, old, _ := oldSignInFixture(t)
	// Every conversation of this second folder lost its transcript.
	gone := carryNamespace{carryID(0xF1), carryID(0xF2)}
	f.record(gone, 31, nil)
	// And one that is not a sign-in folder at all.
	write(t, filepath.Join(f.d.sessionsDir(), "notes.txt"), "x")
	f.s.scanOldSignIns()
	found := f.s.snapshot().OldSignIns
	if len(found) != 1 || found[0].Account != old.Account {
		t.Fatalf("only a folder with something to import is offered: %+v", found)
	}
}

func TestOldSignInImportHasNothingToDoTwiceAndWaitsForAnAccountBeingAdded(t *testing.T) {
	f, old, _ := oldSignInFixture(t)
	if err := f.s.importOldSignIn(old.Account, old.Org); err != nil {
		t.Fatal(err)
	}
	err := f.s.importOldSignIn(old.Account, old.Org)
	if errorCode(err) != "NOTHING_TO_IMPORT" || len(f.pending()) != 0 || !f.running {
		t.Fatalf("a second import finds nothing and leaves things as they are: %v", err)
	}
	f.s.st.AddingAccount = true
	if err := f.s.importOldSignIn(old.Account, old.Org); errorCode(err) != "ADDING_ACCOUNT" {
		t.Fatalf("no import while an account is being added: %v", err)
	}
}
