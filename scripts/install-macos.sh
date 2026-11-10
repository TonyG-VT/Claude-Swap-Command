#!/bin/bash
# Installs the app that build-macos.sh made into ~/Applications.
#
# - The helper that is running is asked to quit the normal way and is waited
#   for. It is never killed: if it is in the middle of something, this stops.
# - The app being replaced is kept next to it as
#   "Claude Swap Command.previous.app". Installing a rebuild of the version
#   that is already installed leaves that kept copy alone.
# - Saved sign-ins, settings and backups (~/.claude-swap-command) are not
#   touched. Claude itself is not touched.
set -euo pipefail
cd "$(dirname "$0")/.."
built="${1:-dist/Claude Swap Command.app}"
target="$HOME/Applications/Claude Swap Command.app"
previous="$HOME/Applications/Claude Swap Command.previous.app"
program="Contents/MacOS/claude-swap-command"
[[ -d "$built" ]] || { echo "There is no built app at $built. Run: bash scripts/build-macos.sh" >&2; exit 1; }
bash scripts/verify-macos.sh "$built"

# Only this user's own helper: another person logged in to this Mac may be
# running theirs.
me="$(/usr/bin/id -u)"
running() { /usr/bin/pgrep -u "$me" -f "/Claude Swap Command.app/$program" > /dev/null; }
if running; then
  echo "Asking the running helper to quit…"
  /usr/bin/pkill -TERM -u "$me" -f "/Claude Swap Command.app/$program" || true
  for _ in $(seq 1 60); do
    running || break
    sleep 1
  done
  if running; then
    echo "The running helper did not quit within a minute (it may be in the middle of a switch)." >&2
    echo "Nothing was installed. Quit it from its menu, then run this again." >&2
    exit 1
  fi
fi

release_of() {
  echo "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$1/Contents/Info.plist" 2>/dev/null) ($(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$1/Contents/Info.plist" 2>/dev/null))"
}

mkdir -p "$HOME/Applications"
kept=""      # where the app that was installed went, so it can come back
aside=""     # a temporary folder, when it only went aside for the moment
if [[ -e "$target" ]]; then
  if [[ "$(release_of "$target")" == "$(release_of "$built")" ]]; then
    # A rebuild of the version that is already installed. The kept copy of
    # the version before it stays exactly as it is.
    aside="$(/usr/bin/mktemp -d "$HOME/Applications/.claude-swap-command-replaced.XXXXXX")"
    mv "$target" "$aside/"
    kept="$aside/$(basename "$target")"
  else
    rm -rf "$previous"
    mv "$target" "$previous"
    kept="$previous"
    echo "Kept the app that was installed ($(release_of "$previous")) as: $previous"
  fi
fi
# If the copy fails, the app that was there goes back, so there is never
# nothing installed.
if ! /usr/bin/ditto "$built" "$target" || ! bash scripts/verify-macos.sh "$target"; then
  rm -rf "$target"
  if [[ -n "$kept" ]]; then
    mv "$kept" "$target"
    [[ -z "$aside" ]] || rmdir "$aside" 2>/dev/null || true
    /usr/bin/open "$target" || true
    echo "The new app could not be put in place. The one that was installed is back and running." >&2
  else
    echo "The new app could not be put in place. Nothing was installed." >&2
  fi
  exit 1
fi
[[ -z "$aside" ]] || rm -rf "$aside"
/usr/bin/open "$target"
echo "Installed and started: $target"
echo "Look for its icon in the menu bar at the top right of the screen."
