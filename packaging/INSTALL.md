# Claude Swap Command: getting started

Claude Swap Command is a small helper that sits in the menu bar of your Mac.
It switches the Claude desktop app between your own Claude accounts without
signing in again, and brings your Code conversations along.

This guide takes you from nothing to your first switch.

## Read this first

- **It is not an Anthropic product, and Anthropic's rules may not allow it.**
  To do its job the helper keeps copies of Claude's sign-in on your Mac, and
  it uses your sign-in to ask Anthropic's service for your account's name,
  plan and usage. Anthropic's published rules say that Claude sign-ins are
  meant for Anthropic's own apps and that developers of other tools may not
  collect or store them. Anthropic also says it can enforce this, and can
  suspend an account that breaks its terms, without notice and without a
  refund. Read the rules yourself before you decide:
  [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) and
  [Claude Code: legal and compliance](https://code.claude.com/docs/en/legal-and-compliance).
  Using this helper is your own decision and your own risk.
- **Use it only with accounts that are yours.** Never put someone else's
  account on your Mac, and never give anyone your sign-ins or a copy of the
  helper's data folder. Anthropic's terms forbid sharing account sign-ins.
- **Nothing leaves your Mac** except the helper's own questions to
  Anthropic's service, each asked with that account's own sign-in. No sign-in
  is sent to the author of this helper or to anyone else.

## What it switches, and what it does not

- **Switches:** which account the Claude desktop app is signed in to.
- **Follows you:** your conversations in the app's **Code** tab. They are
  copied to the account you switch to.
- **Stays with each account:** ordinary chats (they live in each account on
  Anthropic's side); plugins, skills and connectors (each account has its
  own, so work that was using them loses them after a switch); scheduled
  tasks; Remote Control and cloud sessions.
- **Not touched at all:** the `claude` command in a terminal, Claude in a web
  browser and Claude on your phone. They keep their own sign-in.
- **A switch closes Claude and opens it again.** Whatever Claude is doing
  stops. If a reply is still being written, Claude asks you first.

## What you need

- A Mac with macOS 13 or later. So far the helper has been used on one Mac
  only: Apple Silicon, macOS 27. Intel Macs and older macOS are untested.
- The Claude desktop app from Anthropic, in your Applications folder, opened
  and signed in.
- Two or more Claude accounts of your own. Max accounts are the ones tried so
  far; Pro, Team and Enterprise accounts are untested.

## Install

Today the app has to be built on the Mac that will use it. A copy built on
another Mac is refused by macOS, because the app is not signed with an Apple
developer certificate.

1. Install Apple's command-line tools. Open the **Terminal** app, paste this
   line, press Enter, and follow the window that appears:

   ```bash
   xcode-select --install
   ```

2. Install Go, version 1.26 or later: download the macOS installer from
   <https://go.dev/dl/> and run it. Then close Terminal and open it again.
3. Get the code. You need to have been given access to the repository.

   ```bash
   git clone https://github.com/TonyG-VT/Claude-Swap-Command.git
   ```

   ```bash
   cd Claude-Swap-Command
   ```

4. Build and install it. This one line does both; it takes about a minute
   the first time, because it downloads a few small code libraries.

   ```bash
   out="$(mktemp -d)" && BUILD_OUTPUT_DIR="$out" bash scripts/build-macos.sh && bash scripts/install-macos.sh "$out/Claude Swap Command.app"
   ```

The helper is now in the Applications folder inside your home folder, and it
has started. No window opens: look for its small icon in the menu bar at the
top right of the screen.

## The first start: two questions from macOS

1. **Notifications.** macOS asks whether Claude Swap Command may send
   notifications. Choose **Allow**; that is how it tells you a switch is done.
   If you choose not to, it shows a small window instead that closes by
   itself.
2. **Your keychain.** macOS shows a window saying that **security** wants to
   use **Claude Safe Storage** from your keychain, and asks for your Mac login
   password. "security" is Apple's own keychain tool, which the helper uses.
   "Claude Safe Storage" is the key Claude itself uses to scramble its
   sign-in; the helper needs it to see which account is signed in.
   - **Allow** lets the helper read the key this once. macOS asks again each
     time the helper starts.
   - **Always Allow** means you are not asked again. Know what that does:
     from then on, any program running under your Mac login can read that key
     through the same Apple tool without asking you.
   - **Deny** means the helper cannot work. Its menu then says so.

## Set up your accounts

1. Open Claude and sign in as you normally do. Within a minute the account
   appears in the helper's menu as **Account 1**.
2. Click the helper's icon, then **Add account…**, then **Add account**.
   Claude closes and opens again on its sign-in screen. Sign in to your next
   account. It appears in the menu by itself as **Account 2**.
3. Repeat step 2 for each further account.
4. If the helper should start whenever you log in to your Mac, click its icon
   and then **Start at login**.

## Switch

1. Click the helper's icon, then **Switch to Account …**.
2. Click **Switch** in the question that follows (or press Enter).

Claude closes, changes account and opens again. This takes 20 to 30 seconds.
A message says how many conversations came along.

- If Claude is still working on something, it asks in its own window whether
  to quit. Answer it there.
- If Claude does not close, nothing is changed, and the helper says so.
- If Claude shows its sign-in screen after a switch, that saved sign-in has
  expired. Sign in there, or switch back to the account you came from.

## Rules that keep you out of trouble

- **Never use Claude's own Log out to change accounts.** Logging out ends
  that sign-in everywhere, including the copy the helper saved. Use the
  helper's menu instead.
- **Do not copy the helper's data folder** (`.claude-swap-command` in your
  home folder) to another Mac or another person. It holds copies of your
  sign-ins, and they only work on the Mac that made them.
- **Run one copy of the helper**, from the Applications folder.
- **Switch at a stopping point.** A reply that resumes after a switch counts
  against the account you switched to.

## If something looks wrong

- Click the helper's icon, then **Status…**. It says what the helper sees,
  what the last switch did, and why anything was left out.
- A message that ends in a code in capital letters (for example
  `CLAUDE_STILL_OPEN`) names the exact reason. Nothing is changed when a
  switch is refused, and a switch that fails halfway is put back.
- **Fix an interrupted switch…** appears in the menu only if the Mac or the
  helper was shut down in the middle of a switch.
- To ask for help, click **Status…**, then **Copy**, and send that text to
  the person who gave you the helper, with the code you saw. The text
  contains the names and email addresses of your accounts and no sign-in.
  Never send anything from the `accounts` or `backups` folders.

## Updating

In Terminal, inside the `Claude-Swap-Command` folder:

```bash
git pull
```

Then run the build-and-install line from step 4 of **Install** again. The
version you had is kept next to the new one as
`Claude Swap Command.previous.app`. Your accounts and settings stay as they
are.

## Removing it

1. Click the helper's icon. If **Start at login** is ticked, click it to
   untick it. Then click **Quit Claude Swap Command**.
2. Move **Claude Swap Command** from the Applications folder inside your home
   folder to the Trash.
3. Your saved sign-ins and backups are in the folder `.claude-swap-command`
   in your home folder. Delete that folder once you no longer want them.
   Claude itself is not affected: it stays signed in to the account it is on.
4. If you chose **Always Allow** for the keychain question, you can take that
   back: open the **Keychain Access** app (search for it with Spotlight),
   find **Claude Safe Storage**, open it, and under **Access Control** remove
   **security**.
