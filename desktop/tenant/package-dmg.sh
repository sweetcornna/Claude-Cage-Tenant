#!/usr/bin/env bash
# Build and sign Cage Tenant.app, then package it in a signed DMG with
# SHA256SUMS, all in one output folder. Signing order: App first, DMG second.
#
#   CAGE_TENANT_OUTPUT_DIR optional absolute output folder (default _output/tenant-macos).
#                          Use a separate folder for signed releases so they never replace
#                          a test build. Inside the repository it must be under _output.
#   CAGE_SIGNING_IDENTITY  codesign identity; unset or "-" signs ad-hoc (local testing only).
#                          A real identity (Developer ID Application for distribution)
#                          adds a secure timestamp; the App always has the hardened runtime.
#   CAGE_NOTARY_PROFILE    optional notarytool keychain profile; notarizes and staples the DMG.
#   CAGE_NOTARY_KEYCHAIN   optional keychain that holds that profile.
#   CAGE_BUILD_NUMBER      optional CFBundleVersion (default 1).
#
# Never installs, uploads or publishes anything except the optional notarization submission.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_macos
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant/package-dmg.sh  (see the header for environment variables)'
for tool in hdiutil codesign shasum ditto; do
  command -v "$tool" >/dev/null || die "Missing packaging tool: $tool"
done
if [[ -n "${CAGE_NOTARY_PROFILE:-}" && "$identity" == - ]]; then
  die 'Notarization requires CAGE_SIGNING_IDENTITY to be a Developer ID Application identity.'
fi
read_version
dmg_name="Cage-Tenant-$version-macos-universal.dmg"
check_output_artifacts

# 1. Build and sign the App (build.sh verifies it before replacing the old one).
CAGE_TENANT_OUTPUT_DIR="$output_dir" bash "$tenant_dir/build.sh"
verify_app "$app_path"
[[ "$(plutil -extract CFBundleShortVersionString raw -o - "$app_path/Contents/Info.plist")" == "$short_version" ]] \
  || die 'The built App version differs from VERSION.'
app_cdhash="$(codesign --display --verbose=4 "$app_path" 2>&1 | sed -n 's/^CDHash=//p')"

stage="$(mktemp -d "$output_dir/.dmg.XXXXXX")"
mountpoint="$stage/mount"
cleanup() {
  if [[ -d "$mountpoint" ]] && grep -qF " on $mountpoint " <<<"$(mount)"; then
    hdiutil detach -quiet -force "$mountpoint" || true
  fi
  rm -rf "$stage"
}
trap cleanup EXIT

# 2. Lay out the volume: the App, an Applications link, the read-me and license.
root="$stage/root"
mkdir -p "$root"
ditto "$app_path" "$root/$app_name.app"
ln -s /Applications "$root/Applications"
cp "$tenant_dir/dmg-readme.txt" "$root/安装说明 - Read Me.txt"
cp "$tenant_dir/LICENSE" "$root/LICENSE.txt"
codesign --verify --strict "$root/$app_name.app"

# 3. Create, then sign the DMG.
dmg="$stage/$dmg_name"
hdiutil create -quiet -volname "$app_name" -srcfolder "$root" -fs HFS+ -format UDZO -ov "$dmg"
sign=(codesign --force --sign "$identity" --identifier "$dmg_identifier")
while IFS= read -r option; do sign+=("$option"); done < <(signing_options)
"${sign[@]}" "$dmg"
verify_dmg "$dmg"

# 4. The App inside the read-only image is the exact App that was signed.
mkdir -p "$mountpoint"
hdiutil attach -quiet -readonly -nobrowse -noautoopen -mountpoint "$mountpoint" "$dmg"
verify_app "$mountpoint/$app_name.app"
mounted_cdhash="$(codesign --display --verbose=4 "$mountpoint/$app_name.app" 2>&1 | sed -n 's/^CDHash=//p')"
[[ -n "$app_cdhash" && "$mounted_cdhash" == "$app_cdhash" ]] || die 'The App in the DMG differs from the signed build.'
hdiutil detach -quiet "$mountpoint"

# 5. Optional notarization of the DMG, which covers the App inside it.
if [[ -n "${CAGE_NOTARY_PROFILE:-}" ]]; then
  notary=(--keychain-profile "$CAGE_NOTARY_PROFILE")
  if [[ -n "${CAGE_NOTARY_KEYCHAIN:-}" ]]; then notary+=(--keychain "$CAGE_NOTARY_KEYCHAIN"); fi
  xcrun notarytool submit "$dmg" "${notary[@]}" --wait --timeout 20m \
    --output-format json > "$stage/notarization.json"
  notary_status="$(plutil -extract status raw -o - "$stage/notarization.json" 2>/dev/null || true)"
  notary_id="$(plutil -extract id raw -o - "$stage/notarization.json" 2>/dev/null || true)"
  [[ "$notary_status" == Accepted ]] \
    || die "Notarization status: ${notary_status:-unknown}. Details: xcrun notarytool log ${notary_id:-<submission id>}"
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
  verify_dmg "$dmg"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$dmg"
fi

# 6. Move into the output folder (again refusing anything not built here) and
# record the checksum.
check_output_artifacts
(cd "$stage" && shasum -a 256 "$dmg_name" > SHA256SUMS)
mv -f "$dmg" "$output_dir/$dmg_name"
mv -f "$stage/SHA256SUMS" "$output_dir/SHA256SUMS"
(cd "$output_dir" && shasum -a 256 --check --strict SHA256SUMS)
verify_dmg "$output_dir/$dmg_name"
printf 'Packaged: %s\nChecksum: %s\n' "$output_dir/$dmg_name" "$(cut -d' ' -f1 "$output_dir/SHA256SUMS")"
if [[ "$identity" == - ]]; then
  printf 'Note: ad-hoc signature; this DMG is for local testing only and must not be distributed.\n'
elif [[ -z "${CAGE_NOTARY_PROFILE:-}" ]]; then
  printf 'Note: not notarized; Gatekeeper will block it on other Macs until it is notarized and stapled.\n'
fi
