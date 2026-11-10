# Security and private support

This helper handles saved Claude sign-ins. The maintainer is
**Tony Guillaro (`TonyG-VT`)**.

## Reporting a problem

Contact the maintainer privately. If you have access to this private
repository, an issue there is fine. Do not report problems with this
adaptation to the author of the project it is based on, and do not post
anything about sign-ins in public.

Say which version of the helper, of macOS and of Claude you have, what you
did, what you expected and what happened. The text from **Status… → Copy** is
safe to include: it holds account names and emails, counts and codes, and no
sign-in. Never include anything from the `accounts` or `backups` folders, a
cookie, a token, a password or a sign-in code.

## What the helper does and does not do

- It changes Claude's sign-in files only while Claude is fully closed, and
  only inside a backup. A switch that fails halfway is put back. It never
  force-quits Claude.
- It keeps copies of Claude's sign-in (tokens and session cookies) for each
  of your accounts on your Mac. Anthropic's published rules say that
  developers of other tools may not collect or store Claude sign-ins; see
  the notice at the top of the [README](README.md) before using or sharing
  the helper.
- A saved sign-in stays encrypted with Claude's own key, which lives in the
  macOS keychain ("Claude Safe Storage"). The helper reads that key through
  Apple's `security` tool. The data folder is private to your user account,
  but it is not a separate vault: anyone who can use your Mac user account
  can use the saved sign-ins, exactly as they could use Claude itself.
- macOS asks before `security` may read that key. Choosing **Always Allow**
  there has a cost: from then on any program running under that Mac login
  can read Claude's key through the same Apple tool without a question.
  Choosing **Allow** keeps the question, and macOS then asks each time the
  helper starts.
- Before every switch it checks that Claude.app is signed by Anthropic.
- It asks one outside service anything: Anthropic's, for each account's name,
  plan and usage, with that account's own sign-in. It sends no analytics, has
  no update channel of its own and uploads nothing.
- The log and the journal hold account identifiers, counts and codes. They
  hold no token, no cookie, no email address and no conversation title.
- Backups hold copies of sign-in files, so they are deleted when they expire
  (after 14 days, keeping the newest 10), never moved to the Trash.
- It does not switch accounts by itself. Every switch starts with a click
  in its menu and a confirmation.

## Changes that need a careful second look

Anything that touches how sign-ins are read, saved or put in place
(`signin.go`, `profiles.go`, `switch.go`), the backup and put-back code
(`engine.go`, `transaction.go`), which conversations are copied (`carry.go`),
deciding that Claude is closed (`closed.go`), the self-test (`selfcheck.go`),
or what is written to the log and journal (`errcode.go`, `journal.go`).

Add a test for the failure the change could introduce. Never weaken a check
to make a test pass, and never use a real sign-in as a test fixture.
