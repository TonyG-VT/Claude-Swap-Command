package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

func TestErrorCodesSurviveWrapping(t *testing.T) {
	cause := coded("RECORD_SKIPPED", "fixed text")
	wrapped := fmt.Errorf("outer: %w", cause)
	if errorCode(wrapped) != "RECORD_SKIPPED" {
		t.Fatalf("plain wrapping must keep the cause's code, got %s", errorCode(wrapped))
	}
	if errorCode(withCode("ROLLED_BACK", wrapped)) != "RECORD_SKIPPED" {
		t.Fatal("withCode must not replace a more specific code")
	}
	if errorCode(withCode("ROLLED_BACK", errors.New("os error"))) != "ROLLED_BACK" {
		t.Fatal("withCode must code an uncoded error")
	}
	if withCode("X", nil) != nil || errorCode(nil) != "" {
		t.Fatal("nil stays nil")
	}
	if errorCode(errors.New("plain")) != "UNCLASSIFIED" {
		t.Fatal("uncoded errors are reported as UNCLASSIFIED")
	}
	outer := codedf("OUTER", "outer: %w", cause)
	if errorCode(outer) != "OUTER" || !strings.Contains(outer.Error(), "fixed text") || !errors.Is(outer, cause) {
		t.Fatal("codedf keeps the cause's text and chain and names its own code")
	}
}

// A failure message is built from this helper's own sentences plus the class
// of anything a library reported. Raw library text can carry paths, account
// identifiers or file contents, so it must never reach the user, the log or
// the journal.
func TestDescribeCauseShowsOnlyOurTextAndTheClassOfLibraryErrors(t *testing.T) {
	private := "fixture-private-value at /private/fixture/account.json"
	pathErr := &fs.PathError{Op: "open", Path: "/private/fixture/account.json", Err: syscall.EACCES}
	var decoded map[string]any
	jsonErr := json.Unmarshal([]byte(`{"fixture-private-value":`), &decoded)
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"our own sentence":   {coded("X", "Claude is still open, so nothing was changed."), "Claude is still open, so nothing was changed."},
		"raw library text":   {errors.New(private), "an unexpected problem"},
		"permission":         {pathErr, "permission was denied"},
		"missing file":       {fmt.Errorf("read %s: %w", private, fs.ErrNotExist), "a needed file is missing"},
		"disk full":          {&fs.PathError{Op: "write", Path: private, Err: syscall.ENOSPC}, "the disk is full"},
		"invalid json":       {jsonErr, "a file is not valid JSON"},
		"timeout":            {fmt.Errorf("%s: %w", private, context.DeadlineExceeded), "it took too long"},
		"cancelled":          {context.Canceled, "it was cancelled"},
		"sign-in expired":    {errSignedOut, "the sign-in has expired"},
		"rate limited":       {&rateLimited{}, "the service is limiting requests right now"},
		"coded inside plain": {fmt.Errorf("%s: %w", private, coded("Y", "the saved sign-in is missing")), "the saved sign-in is missing"},
	} {
		got := describeCause(tc.err)
		if got != tc.want {
			t.Errorf("%s: describeCause = %q, want %q", name, got, tc.want)
		}
		if strings.Contains(got, "fixture-private") || strings.Contains(got, "/private/") {
			t.Errorf("%s: private text escaped: %q", name, got)
		}
	}
	if describeCause(nil) != "" {
		t.Fatal("no error has no description")
	}

	saved := codedf("SAVE_SIGNIN_FAILED", "The current sign-in could not be saved (%w).", pathErr)
	if saved.Error() != "The current sign-in could not be saved (permission was denied)." {
		t.Fatalf("codedf must describe a wrapped library error by class: %q", saved.Error())
	}
	if !errors.Is(saved, syscall.EACCES) || errorCode(saved) != "SAVE_SIGNIN_FAILED" {
		t.Fatal("codedf keeps the chain and its code")
	}
	wrapped := withCode("ROLLED_BACK", errors.New(private))
	if strings.Contains(wrapped.Error(), "fixture-private") || errorCode(wrapped) != "ROLLED_BACK" {
		t.Fatalf("withCode must not print raw text: %q", wrapped.Error())
	}
	if sanitizeError(wrapped) != "an unexpected problem" {
		t.Fatalf("sanitizeError is the safe description: %q", sanitizeError(wrapped))
	}
}

func TestRedactSecretsRemovesCredentialShapes(t *testing.T) {
	// Credential shapes are assembled at run time so no key-, cookie- or
	// token-shaped literal sits in the source tree.
	fakeKey := "sk-" + "ant-" + strings.Repeat("x", 24)
	fakeJWT := "ey" + strings.Repeat("J", 18) + "." + strings.Repeat("e", 30) + "." + strings.Repeat("s", 40)
	cases := map[string]string{
		"the saved sign-in is missing":                          "the saved sign-in is missing",
		"profile 11111111-2222-4333-8444-555555555555 refused":  "profile 11111111-2222-4333-8444-555555555555 refused",
		"key " + fakeKey + " rejected":                          "key sk-ant-[redacted] rejected",
		"cookie: sessionKey=abc123 expired":                     "cookie=[redacted] expired",
		"Authorization: Bearer " + fakeJWT:                      "Authorization=[redacted]",
		"digest " + strings.Repeat("9ed1359e", 8) + " mismatch": "digest [redacted] mismatch",
	}
	for in, want := range cases {
		if got := redactSecrets(in); got != want {
			t.Errorf("redactSecrets(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	if sanitizeError(nil) != "" {
		t.Fatal("nil error sanitizes to empty")
	}
}
