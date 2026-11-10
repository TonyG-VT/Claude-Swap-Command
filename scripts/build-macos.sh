#!/bin/bash
# Builds "Claude Swap Command.app" into dist/ (or BUILD_OUTPUT_DIR).
# The app is one program with a local (ad-hoc) signature. It is not notarized.
# This script only builds: it does not install anything or touch Claude.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != Darwin ]]; then
  echo "Build this on a Mac." >&2
  exit 1
fi
go_bin="${GO_BIN:-go}"
output_dir="${BUILD_OUTPUT_DIR:-dist}"
app_dir="$output_dir/Claude Swap Command.app"
if [[ -e "$app_dir" ]]; then
  echo "An app already exists at $app_dir. Remove it, or set BUILD_OUTPUT_DIR to a fresh folder." >&2
  exit 1
fi
deployment_target="$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' packaging/Info.plist)"
mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources"
MACOSX_DEPLOYMENT_TARGET="$deployment_target" CGO_ENABLED=1 GOOS=darwin \
  CGO_CFLAGS="-mmacosx-version-min=$deployment_target" CGO_LDFLAGS="-mmacosx-version-min=$deployment_target" \
  "$go_bin" build -trimpath -ldflags '-s -w' -o "$app_dir/Contents/MacOS/claude-swap-command" .
cp packaging/Info.plist "$app_dir/Contents/Info.plist"
cp LICENSE NOTICE "$app_dir/Contents/Resources/"
cp packaging/INSTALL.md "$app_dir/Contents/Resources/INSTALL.md"
licenses="$app_dir/Contents/Resources/ThirdPartyLicenses"
mkdir -p "$licenses"
"$go_bin" list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Dir}}{{end}}{{end}}' . | sort -u > "$licenses/modules.txt"
while IFS=$'\t' read -r module module_dir; do
  [[ -n "$module" ]] || continue
  [[ -n "$module_dir" && -f "$module_dir/LICENSE" ]] || { echo "The license of a bundled dependency could not be found: $module" >&2; exit 1; }
  cp "$module_dir/LICENSE" "$licenses/${module//\//_}.txt"
done < "$licenses/modules.txt"
# The list names packages and versions, never paths on the build machine.
"$go_bin" version -m "$app_dir/Contents/MacOS/claude-swap-command" | awk '$1=="dep" {print $2 " " $3 " " $4}' > "$licenses/modules.txt"
codesign --force --sign - "$app_dir"
bash scripts/verify-macos.sh "$app_dir"
ditto -c -k --keepParent "$app_dir" "$output_dir/Claude-Swap-Command-macos.zip"
(cd "$output_dir" && shasum -a 256 Claude-Swap-Command-macos.zip > Claude-Swap-Command-macos.zip.sha256)
echo "Built $app_dir (local signature; not notarized)."
