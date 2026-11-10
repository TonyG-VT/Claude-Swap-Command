# Claude Swap Command

A small menu-bar helper for the Claude desktop app on a Mac. It switches Claude
between your own accounts without signing in again, brings your Code
conversations along, and shows how much of each account's plan is used.

Version 0.4.1. This is the Mac version. It is built for macOS 13 or later and
does not depend on which version of Claude is installed. So far it has been
used on one Mac only (Apple Silicon, macOS 27, three Max accounts).

**New here? Start with [packaging/INSTALL.md](packaging/INSTALL.md).** It
takes you from nothing to your first switch.

## Before you use or share this

- **It is not an Anthropic product, and Anthropic's rules may not allow it.**
  The helper keeps copies of Claude's sign-in on your Mac and uses your
  sign-in to ask Anthropic's service for your account's name, plan and usage.
  Anthropic's published rules say that Claude sign-ins are meant for
  Anthropic's own apps and that developers of other tools may not collect or
  store them, and that Anthropic can enforce this, and suspend accounts that
  break its terms, without notice or refund. Read them before you decide:
  [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) and
  [Claude Code: legal and compliance](https://code.claude.com/docs/en/legal-and-compliance).
  Everyone who uses the helper takes that risk for their own accounts.
- **Each person uses only their own accounts**, on their own Mac. Sign-ins
  and the helper's data folder are never shared.

## What you see

Everything is in one menu. Click the helper's icon in the menu bar:

```text
Account 2 is active
──────────────────────────────────────────────
Switch to Account 1: Personal (me@example.com, Max)
      5h 0% · week 13%, resets Sat 04:00
✓ Account 2: Work (me@work.example, Max)
      5h 17% · week 83%, resets Tue 13:00
Switch to Account 3: Spare (spare@example.com, Max)
      5h 0% · week 96%, resets Sun 22:59
──────────────────────────────────────────────
Add account…
──────────────────────────────────────────────
Status…
Pause usage checks
Start at login
──────────────────────────────────────────────
Quit Claude Swap Command
```

Two more lines appear only when they apply: **Import conversations from an old
sign-in** and **Fix an interrupted switch…**.

## Switching accounts

1. Click **Switch to Account …** in the menu.
2. Click **Switch** in the one question that follows.

Claude closes, changes account and opens again with your conversations. A
message says what came along.

- If Claude is still writing a reply, it asks its usual "still working"
  question in its own window. Answer it there.
- If Claude does not close, nothing is changed, and the helper says so.
- If a step fails, everything is put back, Claude opens again as it was, and
  you get the reason in plain words with a short code.
- If Claude shows its sign-in screen after a switch (a saved sign-in can
  expire), sign in there, or switch back to the account you came from.

## Adding an account

1. Click **Add account…**, then **Add account** in the question that follows.
2. Claude opens on its sign-in screen. Sign in to the account you want to add.
3. The new account appears in the menu by itself.

The account you were on stays saved. Claude's own **Log out** is never used,
because that would end the saved sign-in.

## How conversations follow you

These are the conversations in Claude's **Code** tab, which Claude keeps on
your Mac. Ordinary chats live in each account on Anthropic's side and do not
move.

- Every conversation on the account you leave is copied to the account you
  land on.
- If the copy on the account you land on has newer messages, that copy is kept.
- If both copies have the same messages and differ only in housekeeping
  details, the copy from the account you are leaving wins, unless the
  difference is a pure cache detail, in which case nothing is touched.
- A conversation that cannot be copied safely is left as it is and listed in
  **Status…** with the reason. It never stops the switch.
- The helper never deletes a conversation. Deleting one in Claude on one
  account does not delete it on the others.

## A Claude update cannot block switching

Earlier versions accepted only exact, pre-approved copies of Claude's program
files, so every Claude update stopped all switching. That check is gone.
Instead the helper tests the few things it actually relies on, before every
switch and whenever Claude changes version:

- Claude.app is Anthropic's own app;
- the sign-in can be read, and only one account is signed in;
- the sign-in cookie is in the file the helper swaps;
- the conversation folder can be read (only needed to carry conversations).

Every switch makes a backup first, and after Claude opens again the helper
checks that the right account really is signed in. If a future Claude update
changes how sign-ins are stored, you are told when Claude updates, and nothing
is touched.

## What is kept on your Mac

Everything stays in `~/.claude-swap-command` on this Mac:

| What | Where |
| --- | --- |
| Saved sign-ins, one per account (still encrypted by Claude's own key) | `accounts/` |
| A backup of every switch (kept 14 days, and at least the newest 10) | `backups/` |
| What each switch did: counts and short codes, never titles or sign-ins | `journal/switches.jsonl` |
| The helper's log | `switcher.log` |
| The helper's settings | `state.json` |

The helper talks to one outside service: Anthropic's own, to read each
account's name, plan and usage with that account's own sign-in. A switch does
not need the network.

## Building and installing

[packaging/INSTALL.md](packaging/INSTALL.md) has the full walk-through for a
Mac that has never had the helper. In short, you need Go 1.26 or later and
Apple's command-line developer tools (`xcode-select --install`), then:

```bash
out="$(mktemp -d)" && BUILD_OUTPUT_DIR="$out" bash scripts/build-macos.sh && bash scripts/install-macos.sh "$out/Claude Swap Command.app"
```

The first script builds the app into a fresh folder and checks it. The second
asks a running helper to quit, keeps the app it replaces as
`Claude Swap Command.previous.app`, puts the new one in `~/Applications` and
starts it. Saved sign-ins and settings are not touched. If Go is not on your
PATH, put `GO_BIN=/path/to/go` in front of the line.

The app is signed locally and is not notarized. That is why it has to be
built on the Mac that uses it: macOS refuses a copy built somewhere else.
A copy that opens on any Mac needs an Apple developer certificate, which this
project does not have.

## Checking without changing anything

Run the program inside the app with one of these. None of them changes
Claude or your accounts.

| Option | What it prints |
| --- | --- |
| `-doctor` | The self-test against the installed Claude, check by check |
| `-dry-run` | What a switch to each other account would carry over, and whether each saved sign-in is usable |
| `-status` | What the Status window shows |
| `-ui-selftest` | Shows each kind of window for two seconds, to prove they work |
| `-version` | The version |

```bash
"$HOME/Applications/Claude Swap Command.app/Contents/MacOS/claude-swap-command" -doctor
```

## Not in this version

- Switching by itself when an account reaches its limit.
- Removing a deleted conversation from the other accounts too.
- Ordinary chats, plugins, skills, connectors, scheduled tasks, Remote
  Control and cloud sessions belong to each account and do not follow a
  switch.
- The `claude` command in a terminal and Claude in a web browser keep their
  own sign-in; the helper does not touch them.
- A Developer ID signature and notarization, so a ready-made app could be
  handed to another Mac.

## For developers

```bash
go vet ./... && go test -race ./...
```

- `switch.go` is the switch from start to finish; `engine.go` and
  `transaction.go` are the backup it runs inside.
- `carry.go` decides which conversations are copied; `signin.go` reads who is
  signed in; `selfcheck.go` is the self-test; `closed.go` decides when Claude
  is really closed.
- `menu.go` and `status.go` work out what the menu and the Status window say;
  `tray.go` and `ui_darwin.go` put it on screen.
- [CONTRIBUTING.md](CONTRIBUTING.md) lists every file and the rules for
  changing this code; [SECURITY.md](SECURITY.md) says what the helper does
  with sign-ins.

## Credits and licence

Based on [claude-account-switcher](https://github.com/SnlperStripes/claude-account-switcher)
by SnlperStripes. See [NOTICE](NOTICE) and [LICENSE](LICENSE).
