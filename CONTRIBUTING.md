# Working on Claude Swap Command

## Build and test

You need a Mac with Go 1.26 or later and Apple's command-line developer tools.

```sh
go vet ./... && go test -race ./...
bash scripts/build-macos.sh
```

The tests never touch the real Claude or a real sign-in: they build pretend
data folders in a temporary place. A few extra checks do look at the real
Mac, read-only, and run only when asked for. The second line also asks
Anthropic's usage service once per account:

```sh
CLAUDE_SWAP_TEST_LIVE_PLAN=1 go test -v -run 'TestLive' .
CLAUDE_SWAP_TEST_LIVE_USAGE=1 go test -v -run 'TestLive' .
```

When the version changes, change it in two places: `version` in `main.go` and
`CFBundleShortVersionString` in `packaging/Info.plist` (and raise
`CFBundleVersion` by one). The build stops if the two disagree.

## How the code is laid out

| File | What it is |
| --- | --- |
| `switch.go` | A switch from start to finish, adding an account, fixing an interrupted switch |
| `engine.go`, `transaction.go` | The backup every change runs inside, and putting things back |
| `profiles.go` | Saving a sign-in and putting a saved one in place |
| `signin.go` | Reading who is signed in |
| `selfcheck.go` | The self-test that decides whether switching is safe on this Claude |
| `closed.go`, `quit_darwin.go` | Asking Claude to quit and knowing when it is really closed |
| `carry.go`, `cache_rule.go` | Which conversations are copied, and the copy itself |
| `records.go` | Reading and writing one conversation file safely |
| `approvals.go`, `spaces.go` | Carrying project groups along with the conversations |
| `old_signin.go` | Importing conversations from a sign-in that is not in the menu |
| `switcher.go`, `account_health.go`, `usage.go` | The helper's state, the usage checks, running one thing at a time |
| `menu.go`, `status.go` | What the menu and the Status window say (no screen code) |
| `tray.go`, `ui_darwin.go` | Putting it on screen: the menu, windows, notifications |
| `errcode.go`, `journal.go` | Error codes with plain reasons, and the record of what was done |

## Rules of the house

- Write the test first and watch it fail. Each rule the helper follows has a
  test named after the rule.
- A message a person will read says what happened in plain words and ends
  with a code. It never contains a library's raw error text, a path into a
  saved sign-in, a token or a cookie. Use `coded` and `codedf`.
- Nothing is written to Claude's files outside a backup, and nothing is
  written while Claude is running.
- The helper never deletes a conversation and never signs an account out.
- No new check may depend on which version of Claude is installed. Test the
  thing you rely on instead (see `selfcheck.go`).
- Read [SECURITY.md](SECURITY.md) before changing anything listed there.
