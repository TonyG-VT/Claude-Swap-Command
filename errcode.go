package main

// Every failure the helper reports carries a stable code next to a sentence
// this helper wrote itself, so the alert, the log and the journal name the
// cause instead of only the outcome.
//
// The text of an error that came from a library (the operating system, the
// JSON decoder, the network stack) is never shown or stored: it can carry
// file paths, account identifiers or file contents. Such an error is
// described by its class only ("permission was denied", "the disk is full").
// redactSecrets is a last net over anything that is about to be written.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os/exec"
	"regexp"
	"syscall"
)

type codedError struct {
	Code string
	Err  error
}

func (e *codedError) Error() string { return e.Err.Error() }
func (e *codedError) Unwrap() error { return e.Err }

// describedError prints the safe description of an error while keeping the
// original in the chain for errors.Is and errors.As.
type describedError struct{ err error }

func (e describedError) Error() string { return describeCause(e.err) }
func (e describedError) Unwrap() error { return e.err }

// coded makes an error from a code and one of this helper's own sentences.
func coded(code, text string) error { return &codedError{Code: code, Err: errors.New(text)} }

// codedf formats one of this helper's own sentences. Any error among the
// arguments is printed as its safe description and stays in the chain; pass
// only values that are safe to show for the other arguments.
func codedf(code, format string, args ...any) error {
	safe := make([]any, len(args))
	for i, arg := range args {
		if err, ok := arg.(error); ok && err != nil {
			safe[i] = describedError{err}
		} else {
			safe[i] = arg
		}
	}
	return &codedError{Code: code, Err: fmt.Errorf(format, safe...)}
}

// withCode gives err a code unless its chain already names a more specific one.
func withCode(code string, err error) error {
	if err == nil {
		return nil
	}
	var c *codedError
	if errors.As(err, &c) {
		return err
	}
	return &codedError{Code: code, Err: describedError{err}}
}

// errorCode returns the outermost code in err's chain. Plain wrappers around
// a coded error keep that error's code.
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var c *codedError
	if errors.As(err, &c) {
		return c.Code
	}
	return "UNCLASSIFIED"
}

// describeCause is the only text form of an error that may be shown, logged
// or journaled.
func describeCause(err error) string {
	if err == nil {
		return ""
	}
	var c *codedError
	if errors.As(err, &c) {
		return c.Error()
	}
	var limited *rateLimited
	var syntax *json.SyntaxError
	var mistyped *json.UnmarshalTypeError
	var network net.Error
	var exit *exec.ExitError
	var launch *exec.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "it took too long"
	case errors.Is(err, context.Canceled):
		return "it was cancelled"
	case errors.Is(err, errSignedOut):
		return "the sign-in has expired"
	case errors.As(err, &limited):
		return "the service is limiting requests right now"
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "the disk is full"
	case errors.Is(err, syscall.EROFS):
		return "the disk is read-only"
	case errors.Is(err, fs.ErrPermission):
		return "permission was denied"
	case errors.Is(err, fs.ErrNotExist):
		return "a needed file is missing"
	case errors.Is(err, fs.ErrExist):
		return "a file was already there"
	case errors.As(err, &syntax), errors.As(err, &mistyped):
		return "a file is not valid JSON"
	case errors.As(err, &network):
		return "the network could not be reached"
	case errors.As(err, &exit):
		return "a system command reported a failure"
	case errors.As(err, &launch):
		return "a system command could not be started"
	}
	var path *fs.PathError
	if errors.As(err, &path) {
		return "a file could not be read or written"
	}
	return "an unexpected problem"
}

var (
	redactAPIKey  = regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]+`)
	redactLabeled = regexp.MustCompile(`(?i)\b(bearer|token|cookie|secret|password|authorization|sessionkey|refresh_token|access_token)\b[\s=:"']+(?:bearer\s+)?[^\s"',;]+`)
	redactOpaque  = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{40,}`)
)

// redactSecrets removes anything shaped like a credential: Anthropic key
// prefixes, labelled token or cookie values and long opaque runs.
func redactSecrets(s string) string {
	s = redactAPIKey.ReplaceAllString(s, "sk-ant-[redacted]")
	s = redactLabeled.ReplaceAllString(s, "$1=[redacted]")
	s = redactOpaque.ReplaceAllString(s, "[redacted]")
	return s
}

// sanitizeError is the text form of err for the alert, the log and the journal.
func sanitizeError(err error) string {
	return redactSecrets(describeCause(err))
}
