package main

import (
	"errors"
	"strings"
	"testing"
)

// The status line is also the switcher.log line. It names the operation, the
// code and the cause, and never raw text from a library error.
func TestRunReportsTheCodeAndTheCause(t *testing.T) {
	s := newSwitcher(t.TempDir())
	s.run("Switch account", func() error {
		return coded("SAVED_SIGNIN_MISSING", "the saved sign-in for that account is missing")
	})
	if got, want := s.snapshot().Status, "Switch account failed [SAVED_SIGNIN_MISSING]: the saved sign-in for that account is missing"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	s.run("Switch account", func() error { return errors.New("fixture-private-value at /private/fixture/account.json") })
	got := s.snapshot().Status
	if got != "Switch account failed [UNCLASSIFIED]: an unexpected problem" || strings.Contains(got, "fixture-private") {
		t.Fatalf("raw error text must not reach the status line: %q", got)
	}
}
