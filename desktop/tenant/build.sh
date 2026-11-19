#!/usr/bin/env bash
# Build the universal (arm64 + x86_64) Cage Tenant.app into _output/tenant-macos,
# or into the absolute folder named by CAGE_TENANT_OUTPUT_DIR.
# Signs ad-hoc unless CAGE_SIGNING_IDENTITY is set. Never installs anything.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_macos
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant/build.sh  (set CAGE_SIGNING_IDENTITY to sign with a real identity)'
for tool in swiftc lipo codesign plutil iconutil otool; do
  command -v "$tool" >/dev/null || die "Missing build tool: $tool"
done
read_version
check_output_artifacts

mkdir -p "$output_dir"
stage="$(mktemp -d "$output_dir/.build.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
bundle="$stage/$app_name.app"
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources"

# Compile each slice for macOS 14 in the Swift 6 language mode, then merge.
export MACOSX_DEPLOYMENT_TARGET="$deployment_target"
slices=()
for arch in "${architectures[@]}"; do
  swiftc -O -swift-version 6 -target "$arch-apple-macosx$deployment_target" \
    "${frameworks[@]}" "${app_sources[@]}" -o "$stage/$executable-$arch"
  minimum="$(otool -l "$stage/$executable-$arch" | awk '$1 == "minos" {print $2; exit}')"
  [[ "$minimum" == "$deployment_target" ]] || die "Unexpected $arch deployment target: ${minimum:-none}"
  slices+=("$stage/$executable-$arch")
done
lipo -create "${slices[@]}" -output "$bundle/Contents/MacOS/$executable"

plist="$bundle/Contents/Info.plist"
cp "$tenant_dir/Info.plist" "$plist"
plutil -replace CFBundleShortVersionString -string "$short_version" "$plist"
plutil -replace CFBundleVersion -string "$build_number" "$plist"
plutil -convert xml1 "$plist"
plutil -lint "$plist" >/dev/null
[[ "$(plutil -extract CFBundleExecutable raw -o - "$plist")" == "$executable" ]] || die 'Info.plist executable mismatch.'
printf 'APPL????' > "$bundle/Contents/PkgInfo"

# Same artwork as Cage, rendered by the existing generator; the App name and
# bundle identifier keep the two applications apart.
swiftc -O "$repo_dir/desktop/Icon.swift" -o "$stage/icon-tool"
"$stage/icon-tool" "$repo_dir/logo.png" "$stage/$executable.iconset"
iconutil -c icns "$stage/$executable.iconset" -o "$bundle/Contents/Resources/$executable.icns"
cp "$tenant_dir/LICENSE" "$bundle/Contents/Resources/LICENSE"

# One executable and no nested code, so signing the bundle seals everything.
sign=(codesign --force --sign "$identity" --options runtime
  --entitlements "$tenant_dir/CageTenant.entitlements")
while IFS= read -r option; do sign+=("$option"); done < <(signing_options)
"${sign[@]}" "$bundle"
verify_app "$bundle"

# Replace the previous build only after the new one verifies, and only if it
# is still an unmodified Cage Tenant App that is not running.
check_output_artifacts
if [[ -e "$app_path" ]]; then mv "$app_path" "$stage/previous.app"; fi
if ! mv "$bundle" "$app_path"; then
  if [[ -d "$stage/previous.app" ]]; then mv "$stage/previous.app" "$app_path"; fi
  die "Could not move the App into $output_dir."
fi
printf 'Built: %s\n  version %s (%s), slices: %s, signature: %s\n' "$app_path" "$short_version" \
  "$build_number" "$(lipo -archs "$app_path/Contents/MacOS/$executable")" \
  "$([[ "$identity" == - ]] && echo ad-hoc || echo "$identity")"
