#!/bin/bash
# Checks a built "Claude Swap Command.app". It looks only at the app it is
# given: it never opens Claude and never reads any sign-in.
set -euo pipefail
app_dir="${1:?Usage: verify-macos.sh APP_PATH}"
plist="$app_dir/Contents/Info.plist"
binary="$app_dir/Contents/MacOS/claude-swap-command"
fail() { echo "$1" >&2; exit 1; }
[[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plist")" == com.tonyg-vt.claude-swap-command ]] || fail "The app has an unexpected identifier."
[[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist")" == claude-swap-command ]] || fail "The app names an unexpected program."
[[ "$(/usr/libexec/PlistBuddy -c 'Print :LSUIElement' "$plist")" == true ]] || fail "The app must be a menu-bar app."
[[ -x "$binary" ]] || fail "The app's program is missing."
executables="$(/usr/bin/find "$app_dir" -type f \( -perm -100 -o -perm -010 -o -perm -001 \) | /usr/bin/wc -l | /usr/bin/tr -d ' ')"
[[ "$executables" == 1 ]] || fail "The app must contain exactly one program; found $executables."
[[ ! -e "$app_dir/Contents/Helpers" ]] || fail "The app must not contain helper programs."
minimum="$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' "$plist")"
actual="$(/usr/bin/otool -l "$binary" | awk '/LC_BUILD_VERSION/{found=1;next} found && $1=="minos" {print $2;exit}')"
[[ "$actual" == "$minimum" ]] || fail "The program was built for macOS $actual but the app says $minimum."
/usr/bin/codesign --verify --deep --strict "$app_dir"
expected="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist")"
[[ "$("$binary" -version)" == "Claude Swap Command $expected" ]] || fail "The program and the app disagree about the version."
for file in LICENSE NOTICE INSTALL.md ThirdPartyLicenses/modules.txt; do
  [[ -f "$app_dir/Contents/Resources/$file" ]] || fail "The app is missing $file."
done
echo "The app checks out (version $expected, $(/usr/bin/lipo -archs "$binary"), macOS $minimum or later)."
