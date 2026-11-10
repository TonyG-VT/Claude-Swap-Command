package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#include <stdlib.h>

// csc_request_quit asks every running copy of the app with this bundle
// identifier to quit the normal way, the same request the Dock's Quit sends.
// The app may show its own "still working" question. Returns how many copies
// accepted the request.
static int csc_request_quit(const char *bundleID) {
	@autoreleasepool {
		NSString *identifier = [NSString stringWithUTF8String:bundleID];
		int asked = 0;
		for (NSRunningApplication *app in [NSRunningApplication runningApplicationsWithBundleIdentifier:identifier]) {
			if ([app terminate]) {
				asked++;
			}
		}
		return asked;
	}
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

// requestQuitDirect sends the quit request through AppKit and reports how
// many running copies accepted it. This needs no macOS permission.
func requestQuitDirect(bundleID string) int {
	id := C.CString(bundleID)
	defer C.free(unsafe.Pointer(id))
	return int(C.csc_request_quit(id))
}

// requestAppQuit asks an app to quit normally. It never forces anything.
func requestAppQuit(bundleID string) error {
	if requestQuitDirect(bundleID) > 0 {
		return nil
	}
	// Fall back to the same polite request sent as an Apple Event. macOS may
	// ask once whether the helper may control the app.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application id "`+bundleID+`" to quit`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	if err := cmd.Start(); err != nil {
		cancel()
		return errors.New("the quit request could not be sent")
	}
	// The request is delivered while this returns; the script is reaped later.
	go func() {
		_ = cmd.Wait()
		cancel()
	}()
	return nil
}

// processTable lists every running program by its executable path. Command
// arguments are never read.
func processTable(ctx context.Context) ([]procEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,comm=")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("the list of running programs is unavailable")
	}
	var table []procEntry
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		if e1 != nil || e2 != nil {
			return nil, errors.New("the list of running programs could not be understood")
		}
		rest := strings.TrimLeft(line, " ")
		rest = strings.TrimLeft(strings.TrimPrefix(rest, fields[0]), " ")
		rest = strings.TrimLeft(strings.TrimPrefix(rest, fields[1]), " ")
		table = append(table, procEntry{PID: pid, Parent: parent, Path: rest})
	}
	return table, nil
}

// signInFileMarks changes whenever Claude's settings or cookie files are
// rewritten, so the quiet time can start again.
func signInFileMarks(d desktop) string {
	var marks []string
	for _, path := range append([]string{d.configPath()}, d.cookieFiles()...) {
		if info, err := os.Lstat(path); err == nil {
			marks = append(marks, fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size()))
		} else {
			marks = append(marks, "-")
		}
	}
	return strings.Join(marks, " ")
}

// liveCloseEnv closes the real Claude on this Mac.
func liveCloseEnv(onWait func([]string)) closeEnv {
	d := claudeDesktop()
	return closeEnv{
		blockers: func(ctx context.Context) ([]string, error) {
			table, err := processTable(ctx)
			if err != nil {
				return nil, err
			}
			return claudeBlockers(table, d.dir), nil
		},
		requestQuit: func() error { return requestAppQuit(claudeBundleID) },
		quietMarks:  func() string { return signInFileMarks(d) },
		sleep: func(ctx context.Context, wait time.Duration) error {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		now:    time.Now,
		onWait: onWait,
		poll:   time.Second,
		quiet:  5 * time.Second,
		limit:  5 * time.Minute,
	}
}
