// Modified for Claude Swap Command (2026).
package main

// The menu-bar menu. It is the whole interface: every account with its
// usage, one click and one confirmation to switch, and a Status window.
// What the menu says is worked out in menu.go; this file only puts it on
// screen and hands each click on.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

type tray struct {
	s           *switcher
	header      *systray.MenuItem
	rows        [menuAccountSlots]*systray.MenuItem
	details     [menuAccountSlots]*systray.MenuItem
	add         *systray.MenuItem
	importMenu  *systray.MenuItem
	importItems [menuImportSlots]*systray.MenuItem
	fix         *systray.MenuItem
	pause       *systray.MenuItem
	autostart   *systray.MenuItem

	mu       sync.Mutex
	model    menuModel
	lastIcon string
	updates  chan struct{}
}

// detailIndent sets the usage line apart from the account's name above it.
const detailIndent = "      "

func (t *tray) onReady() {
	// Set before anything can click or refresh.
	t.s.notify = func(title, body string) { go notifyUser(title, body) }
	t.s.onChange = func() {
		select {
		case t.updates <- struct{}{}:
		default:
		}
	}
	systray.SetTooltip(appTitle)
	t.setIcon(-1, defaultThreshold)
	t.header = systray.AddMenuItem("Starting…", "")
	t.header.Disable()
	systray.AddSeparator()
	for i := range t.rows {
		t.rows[i] = systray.AddMenuItemCheckbox("", "Close Claude, change to this account and open Claude again", false)
		t.rows[i].Hide()
		t.details[i] = systray.AddMenuItem("", "How much of this account's 5-hour and weekly limits is used")
		t.details[i].Disable()
		t.details[i].Hide()
		go t.watchAccount(i)
	}
	systray.AddSeparator()
	t.add = systray.AddMenuItem("Add account…", "Save the current sign-in and open Claude on its sign-in screen")
	t.importMenu = systray.AddMenuItem("Import conversations from an old sign-in", "Conversations filed under a sign-in that is not in this menu")
	for i := range t.importItems {
		t.importItems[i] = t.importMenu.AddSubMenuItem("", "Copy these conversations into the account that is active now")
		t.importItems[i].Hide()
		go t.watchImport(i)
	}
	t.importMenu.Hide()
	t.fix = systray.AddMenuItem("Fix an interrupted switch…", "An earlier switch stopped halfway; choose what to do about it")
	t.fix.Hide()
	systray.AddSeparator()
	status := systray.AddMenuItem("Status…", "What the helper sees and what the last switch did")
	t.pause = systray.AddMenuItemCheckbox("Pause usage checks", "Stop asking how much of each account is used; switching still works", false)
	t.autostart = systray.AddMenuItemCheckbox("Start at login", "Open Claude Swap Command when you log in to this Mac", autostartEnabled())
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit Claude Swap Command", "Close this helper; Claude keeps running")

	// Each click is handed on at once so the menu never waits for anything.
	go func() {
		for {
			select {
			case <-t.add.ClickedCh:
				go t.addClicked()
			case <-t.fix.ClickedCh:
				go t.fixClicked()
			case <-status.ClickedCh:
				go t.statusClicked()
			case <-t.pause.ClickedCh:
				go t.pauseClicked()
			case <-t.autostart.ClickedCh:
				go t.autostartClicked()
			case <-systray.TrayOpenedCh:
				go t.menuOpened()
			case <-quit.ClickedCh:
				if !t.s.busy.CompareAndSwap(false, true) {
					go problemDialog("Still working", "The helper is in the middle of something. Wait until the menu's first line says it is done, then quit.")
					continue
				}
				systray.Quit()
				return
			}
		}
	}()
	go func() {
		for range t.updates {
			t.render()
		}
	}()
	t.s.onChange()
	prepareNotifications()
	go t.loop()
}

func (t *tray) loop() {
	var checked, watched, scanned time.Time
	for ; ; time.Sleep(5 * time.Second) {
		now := time.Now()
		if now.Sub(watched) >= time.Minute {
			watched = now
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			t.s.watchClaude(ctx)
			cancel()
		}
		if t.s.adding() || now.Sub(checked) >= hotEvery {
			checked = now
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			t.s.check(ctx)
			cancel()
		}
		if now.Sub(scanned) >= 5*time.Minute {
			scanned = now
			t.s.scanOldSignIns()
		}
	}
}

// menuOpened brings the menu up to date the moment it is opened, without
// the network: who is signed in, and how old the numbers are.
func (t *tray) menuOpened() {
	if !t.s.busy.Load() {
		t.s.noticeSignIn()
	}
	t.s.onChange()
}

func (t *tray) currentModel() menuModel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.model
}

func setShown(item *systray.MenuItem, shown bool) {
	if shown {
		item.Show()
	} else {
		item.Hide()
	}
}

func setEnabled(item *systray.MenuItem, enabled bool) {
	if enabled == item.Disabled() {
		if enabled {
			item.Enable()
		} else {
			item.Disable()
		}
	}
}

func setChecked(item *systray.MenuItem, on bool) {
	if on != item.Checked() {
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}

func (t *tray) render() {
	snap := t.s.snapshot()
	m := buildMenu(snap, time.Now())
	t.mu.Lock()
	t.model = m
	t.mu.Unlock()

	t.header.SetTitle(m.Header)
	for i := range t.rows {
		if i >= len(m.Accounts) {
			t.rows[i].Hide()
			t.details[i].Hide()
			continue
		}
		row := m.Accounts[i]
		t.rows[i].SetTitle(row.Title)
		t.details[i].SetTitle(detailIndent + row.Detail)
		setChecked(t.rows[i], row.Active)
		setEnabled(t.rows[i], row.Enabled)
		t.rows[i].Show()
		t.details[i].Show()
	}
	t.add.SetTitle(m.AddTitle)
	setEnabled(t.add, m.AddEnabled)
	for i := range t.importItems {
		if i >= len(m.Imports) {
			t.importItems[i].Hide()
			continue
		}
		t.importItems[i].SetTitle(m.Imports[i].Title)
		setEnabled(t.importItems[i], !snap.Busy)
		t.importItems[i].Show()
	}
	setShown(t.importMenu, len(m.Imports) > 0)
	setShown(t.fix, m.ShowFix)
	setEnabled(t.fix, !snap.Busy)
	setChecked(t.pause, m.Paused)
	systray.SetTooltip(m.Tooltip)
	t.setIcon(m.IconPercent, snap.Threshold)
}

func (t *tray) setIcon(pct, threshold float64) {
	key := fmt.Sprintf("%.0f/%.0f", pct, threshold)
	if key == t.lastIcon {
		return
	}
	t.lastIcon = key
	systray.SetIcon(trayIcon(pct, threshold))
}

// report tells the person how something they started from the menu ended.
func (t *tray) report(what string, err error) {
	if err == nil {
		return
	}
	title := what + " did not work"
	switch errorCode(err) {
	case "BUSY":
		return
	case "SIGNIN_NOT_ACCEPTED":
		title = "Claude is asking you to sign in"
	case "DIFFERENT_ACCOUNT":
		title = "Claude is on a different account"
	case "CLAUDE_STILL_OPEN", "SWITCH_CANCELLED":
		title = "Nothing was changed"
	}
	problemDialog(title, fmt.Sprintf("%s\n\nCode: %s", sanitizeError(err), errorCode(err)))
}

func (t *tray) watchAccount(i int) {
	for range t.rows[i].ClickedCh {
		go t.switchClicked(i)
	}
}

func (t *tray) switchClicked(i int) {
	m := t.currentModel()
	if i >= len(m.Accounts) || !m.Accounts[i].Enabled {
		return
	}
	row := m.Accounts[i]
	name := strings.TrimPrefix(row.Title, "Switch to ")
	if !confirmDialog("Switch to "+name+"?",
		"Claude will close, change to this account and open again. Your conversations come with you.\n\nIf Claude is still working on something, it asks you first in its own window.", "Switch") {
		return
	}
	err := t.s.run("Switch account", func() error { return t.s.switchTo(row.ID) })
	if err == nil {
		title, body := t.s.switchMessage()
		t.s.tell(title, body, title+": "+body)
	}
	t.report("The switch", err)
}

func (t *tray) addClicked() {
	if !confirmDialog("Add another account?",
		"Claude will close and open again on its sign-in screen. Sign in there to the account you want to add. It then appears in this menu by itself.\n\nThe account you are on now stays saved, and you can switch back to it at any time.", "Add account") {
		return
	}
	t.report("Adding an account", t.s.run("Add account", t.s.addAccount))
}

func (t *tray) fixClicked() {
	choice := showAlert(alertInfo, "Fix an interrupted switch",
		"An earlier switch stopped halfway, because the helper or the Mac was shut down in the middle.\n\nPut back: Claude closes, and its sign-in files go back to how they were before that switch.\n\nKeep as it is: nothing is changed; the helper just stops waiting.\n\nIf Claude is working normally right now, choose Keep as it is.",
		"", 0, "Keep as it is", "Put back", "Cancel")
	if choice != 0 && choice != 1 {
		return
	}
	t.report("Fixing the interrupted switch", t.s.run("Fix an interrupted switch", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return t.s.fixInterruptedSwitch(ctx, choice == 1)
	}))
}

func (t *tray) statusClicked() {
	if !t.s.busy.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.s.noteSelfCheck(t.s.env.selfCheck(ctx))
		cancel()
		t.s.noticeSignIn()
	}
	report := t.s.statusReport(time.Now())
	switch showAlert(alertInfo, appTitle, "", report, 0, "Close", "Copy", "Open data folder") {
	case 1:
		copyToClipboard(report)
	case 2:
		shellOpen(t.s.dir)
	}
}

func (t *tray) pauseClicked() {
	if err := t.s.setUsageChecksPaused(!t.s.snapshot().Paused); err != nil {
		problemDialog("That setting could not be saved", sanitizeError(err))
	}
	t.s.onChange()
}

func (t *tray) autostartClicked() {
	if err := setAutostart(!autostartEnabled()); err != nil {
		problemDialog("Start at login could not be changed", sanitizeError(err))
	}
	setChecked(t.autostart, autostartEnabled())
}

func (t *tray) watchImport(i int) {
	for range t.importItems[i].ClickedCh {
		go t.importClicked(i)
	}
}

func (t *tray) importClicked(i int) {
	m := t.currentModel()
	if i >= len(m.Imports) {
		return
	}
	source := m.Imports[i]
	if !confirmDialog("Import these conversations?",
		source.Title+"\n\nThey are copied into the account that is active now. Claude closes for a moment and opens again on the same account. Nothing in the old sign-in is removed.", "Import") {
		return
	}
	t.report("The import", t.s.run("Import conversations", func() error { return t.s.importOldSignIn(source.Account, source.Org) }))
}
