// Modified for Claude Swap Command (2026).
// Claude Swap Command is a menu-bar helper for the Claude desktop app on
// macOS. It switches the app between your own signed-in accounts without
// signing in again, brings your conversations along and shows each account's
// plan usage.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"fyne.io/systray"
)

const version = "0.4.1"

func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude-swap-command"), nil
}

func main() {
	status := flag.Bool("status", false, "print what the Status window shows, then exit; changes nothing")
	doctor := flag.Bool("doctor", false, "run the self-test against the installed Claude and print each check; changes nothing")
	dryRun := flag.Bool("dry-run", false, "print what a switch to each other account would carry over; changes nothing")
	uiSelfTest := flag.Bool("ui-selftest", false, "briefly show each kind of window the helper uses, to prove they work on this Mac")
	showVersion := flag.Bool("version", false, "show the version")
	flag.Parse()
	modes := 0
	for _, on := range []bool{*status, *doctor, *dryRun, *uiSelfTest, *showVersion} {
		if on {
			modes++
		}
	}
	if modes > 1 || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "Choose one option at a time. Run with -help to see them.")
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println("Claude Swap Command " + version)
		return
	}
	if runtime.GOOS != "darwin" {
		fmt.Println("This build is for macOS.")
		return
	}
	dir, err := dataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "The home folder could not be found.")
		os.Exit(1)
	}
	switch {
	case *doctor:
		if !printDoctor(os.Stdout) {
			os.Exit(1)
		}
		return
	case *status:
		printStatus(dir, os.Stdout)
		return
	case *dryRun:
		if !printDryRun(dir, os.Stdout) {
			os.Exit(1)
		}
		return
	case *uiSelfTest:
		runUISelfTest(os.Stdout)
		return
	}

	lock, ok := lockInstance(instancePort)
	if !ok {
		return
	}
	defer lock.Close()
	// Even log creation and private-directory maintenance happen only after
	// excluding another running copy.
	if err := ensurePrivateDirectory(dir); err != nil {
		log.Fatal(err)
	}
	setupLog(filepath.Join(dir, "switcher.log"), false)
	log.Printf("Claude Swap Command %s", version)
	if err := keepStateFromBefore(dir); err != nil {
		log.Printf("the earlier settings file could not be kept: %s", describeCause(err))
	}

	t := &tray{s: newSwitcher(dir), updates: make(chan struct{}, 1)}
	go serveInstance(lock, nil)
	shutdownDone := make(chan struct{})
	systray.Run(func() {
		t.onReady()
		// An ordinary request to stop must not interrupt a switch or a save.
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		go func() {
			defer signal.Stop(signals)
			select {
			case <-shutdownDone:
				return
			case <-signals:
				if reserveHelperShutdown(context.Background(), t.s) {
					systray.Quit()
				}
			}
		}()
	}, func() { close(shutdownDone) })
}

// setupLog writes to switcher.log and starts it over once it passes 1 MB.
func setupLog(path string, console bool) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return
	}
	if console {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	} else {
		log.SetOutput(f)
	}
}
