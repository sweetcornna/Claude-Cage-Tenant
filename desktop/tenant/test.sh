#!/usr/bin/env bash
# Check Cage Tenant: Swift 6 typecheck of both slices, unit tests, bundle
# metadata and script syntax. Leaves nothing behind outside a staging folder
# in _output/tenant-macos that is removed on exit. Does not build the App.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_macos
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant/test.sh'

mkdir -p "$output_dir"
stage="$(mktemp -d "$output_dir/.test.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

# The tests replace main.swift with their own entry point.
test_sources=("$tenant_dir/Tests/TenantTests.swift")
for source in "${app_sources[@]}"; do
  [[ "$source" == */main.swift ]] || test_sources+=("$source")
done
for arch in "${architectures[@]}"; do
  target="$arch-apple-macosx$deployment_target"
  swiftc -typecheck -swift-version 6 -warnings-as-errors -target "$target" "${app_sources[@]}"
  swiftc -swift-version 6 -parse-as-library -target "$target" "${frameworks[@]}" \
    "${test_sources[@]}" -o "$stage/tenant-tests-$arch"
  printf 'Swift 6 %s: typecheck and test build passed\n' "$arch"
done

host="$(uname -m)"
for arch in "${architectures[@]}"; do
  binary="$stage/tenant-tests-$arch"
  if [[ "$arch" == "$host" ]]; then
    printf 'Unit tests (%s): ' "$arch"; "$binary"
  elif [[ "$arch" == x86_64 && "$host" == arm64 ]] && /usr/bin/pgrep -q oahd; then
    printf 'Unit tests (%s, Rosetta): ' "$arch"; arch -x86_64 "$binary"
  else
    printf 'Unit tests (%s): skipped, this Mac cannot run %s code; compiled only\n' "$arch" "$arch"
  fi
done

# Bundle metadata: identity, platform floor, and none of the keys that would
# add URL handlers, network exceptions, update feeds or extra permissions.
plist="$tenant_dir/Info.plist"
plutil -lint "$plist" "$tenant_dir/CageTenant.entitlements" >/dev/null
value() { plutil -extract "$1" raw -o - "$plist"; }
[[ "$(value CFBundleIdentifier)" == "$bundle_id" ]] || die 'Info.plist: wrong bundle identifier.'
[[ "$(value CFBundleName)" == "$app_name" ]] || die 'Info.plist: wrong bundle name.'
[[ "$(value CFBundleExecutable)" == "$executable" ]] || die 'Info.plist: wrong executable.'
[[ "$(value LSMinimumSystemVersion)" == "$deployment_target" ]] || die 'Info.plist: wrong minimum macOS.'
[[ "$(value CFBundlePackageType)" == APPL ]] || die 'Info.plist: not an application.'
for key in NSAppTransportSecurity CFBundleURLTypes CFBundleDocumentTypes UTExportedTypeDeclarations \
  SUFeedURL SUPublicEDKey SUEnableAutomaticChecks NSServices LSUIElement LSBackgroundOnly NSMainNibFile; do
  if plutil -extract "$key" raw -o - "$plist" >/dev/null 2>&1; then die "Info.plist: unexpected key $key."; fi
done
entitlements="$(entitlement_lines "$tenant_dir/CageTenant.entitlements")"
[[ "$entitlements" == "$expected_entitlements" ]] || die "Unexpected entitlements: $entitlements"
cp "$tenant_dir/CageTenant.entitlements" "$stage/extra.entitlements"
/usr/libexec/PlistBuddy -c 'Add :com.apple.security.files.user-selected.read-only bool true' "$stage/extra.entitlements"
[[ "$(entitlement_lines "$stage/extra.entitlements")" != "$expected_entitlements" ]] \
  || die 'The entitlement check does not notice an extra entitlement.'
grep -q '^MIT License$' "$tenant_dir/LICENSE" || die 'LICENSE is not the MIT license.'
# The App shows the portal in a WKWebView with an in-memory store and no
# native bridge. Its code must not read cookies, credentials or page content,
# override TLS trust, persist website data, or update itself. Only the
# native local Cage handoff may download or open an external app or DMG.
forbidden='WKScriptMessageHandler|addScriptMessageHandler|WKUserScript|addUserScript|evaluateJavaScript'
forbidden+='|callAsyncJavaScript|WKContentWorld|httpCookieStore|HTTPCookie|Set-Cookie|fetchDataRecords'
forbidden+='|allHeaderFields|SecItem|URLCredential|URLAuthenticationChallenge|SecTrust|serverTrust'
forbidden+='|WKWebsiteDataStore[.]default|forIdentifier|javaScriptCanOpenWindowsAutomatically = true'
forbidden+='|isInspectable|WKDownload|NSURLConnection|import Network|Sparkle'
if grep -nE "$forbidden" "${app_sources[@]}"; then
  die 'App sources use an API outside the portal window boundary (see the matches above).'
fi
for source in "${app_sources[@]}"; do
  [[ "$source" == */LocalCage.swift ]] && continue
  if grep -nE 'NSWorkspace|URLSession' "$source"; then
    die 'Only the native local Cage handoff may download or open applications.'
  fi
done
grep -q 'websiteDataStore = .nonPersistent()' "$tenant_dir/PortalWebWindow.swift" \
  || die 'The portal window must use a non-persistent website data store.'
printf 'Bundle metadata, entitlements and API boundary: passed\n'

# Output folder selection (CAGE_TENANT_OUTPUT_DIR) and the checks that keep
# the scripts from replacing or mixing with artifacts they did not build.
outcome() {  # outcome DIR [check]: "ok" or "fail" from sourcing common.sh
  if CAGE_TENANT_OUTPUT_DIR="$1" bash -c 'source "$1/common.sh" && ${2:-:}' _ "$tenant_dir" "${2:-}" \
    >/dev/null 2>&1; then echo ok; else echo fail; fi
}
expect_outcome() {  # expect_outcome ok|fail DIR [check]
  local got
  got="$(outcome "$2" "${3:-}")"
  [[ "$got" == "$1" ]] || die "CAGE_TENANT_OUTPUT_DIR=$2 ${3:-}: expected $1, got $got"
}
ln -s "$repo_dir" "$stage/repo-link"
for rejected in relative/out "$repo_dir" "$repo_dir/" "$repo_dir/desktop/tenant" "$repo_dir/_output" \
  "$(dirname "$repo_dir")" / "$HOME" /Applications "/Applications/Cage Tenant" "$HOME/Applications/x" \
  "$stage/repo-link" "$stage/repo-link/_output-x" "$stage/../escape" "$stage/./x" "$tenant_dir/Info.plist"; do
  expect_outcome fail "$rejected"
done
for accepted in '' "$repo_dir/_output/tenant-macos" "$stage/release" "$stage/new/nested/release" \
  "$stage/repo-link/_output/tenant-release"; do
  expect_outcome ok "$accepted"
done

fixture="$stage/fixture"
mkdir -p "$fixture"
expect_outcome ok "$fixture" check_output_artifacts
mkdir -p "$fixture/Other.app"
expect_outcome fail "$fixture" check_output_artifacts
rm -rf "$fixture/Other.app"
fake_app="$fixture/$app_name.app"
mkdir -p "$fake_app/Contents/MacOS"
cp "$stage/tenant-tests-$host" "$fake_app/Contents/MacOS/$executable"
plutil -create xml1 "$fake_app/Contents/Info.plist"
plutil -insert CFBundleExecutable -string "$executable" "$fake_app/Contents/Info.plist"
plutil -insert CFBundleIdentifier -string io.example.other "$fake_app/Contents/Info.plist"
codesign --force --sign - "$fake_app" 2>/dev/null
expect_outcome fail "$fixture" check_output_artifacts   # another App under our name
plutil -replace CFBundleIdentifier -string "$bundle_id" "$fake_app/Contents/Info.plist"
expect_outcome fail "$fixture" check_output_artifacts   # signature no longer valid
codesign --force --sign - "$fake_app" 2>/dev/null
expect_outcome ok "$fixture" check_output_artifacts
mkdir -p "$fake_app/Contents/Resources" && echo extra > "$fake_app/Contents/Resources/extra"
expect_outcome fail "$fixture" check_output_artifacts   # modified after signing
rm -rf "$fake_app"

mkdir -p "$stage/volume" && echo test > "$stage/volume/file"
dmg="$fixture/Cage-Tenant-0.0.0-macos-universal.dmg"
hdiutil create -quiet -volname test -srcfolder "$stage/volume" -fs HFS+ -format UDZO "$dmg"
expect_outcome fail "$fixture" check_output_artifacts   # unsigned
codesign --force --sign - --identifier io.example.other "$dmg" 2>/dev/null
expect_outcome fail "$fixture" check_output_artifacts   # another project's identifier
codesign --force --sign - --identifier "$dmg_identifier" "$dmg" 2>/dev/null
expect_outcome ok "$fixture" check_output_artifacts
cp "$dmg" "$fixture/other.dmg"
expect_outcome fail "$fixture" check_output_artifacts   # foreign DMG name
rm "$fixture/other.dmg"

(cd "$fixture" && shasum -a 256 "${dmg##*/}" > SHA256SUMS)
expect_outcome ok "$fixture" check_output_artifacts
echo "$(printf '0%.0s' {1..64})  other.zip" >> "$fixture/SHA256SUMS"
expect_outcome fail "$fixture" check_output_artifacts
printf 'Output folder selection and artifact checks: passed\n'

for script in common.sh build.sh package-dmg.sh test.sh; do bash -n "$tenant_dir/$script"; done
if command -v shellcheck >/dev/null; then
  (cd "$tenant_dir" && shellcheck -x common.sh build.sh package-dmg.sh test.sh)
  printf 'Shell scripts: syntax and shellcheck passed\n'
else
  printf 'Shell scripts: syntax passed (shellcheck not installed)\n'
fi
