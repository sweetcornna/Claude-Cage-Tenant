# Shared settings for the Cage Tenant scripts. Source this file; do not run it.
# shellcheck shell=bash
# shellcheck disable=SC2034  # variables are used by the scripts that source this file

tenant_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_dir="$(cd "$tenant_dir/../.." && pwd -P)"
app_name='Cage Tenant'
bundle_id='io.github.sweetcornna.claude-cage.tenant'
dmg_identifier="$bundle_id.dmg"
executable='CageTenant'
architectures=(arm64 x86_64)
deployment_target='14.0'
app_sources=(
  "$tenant_dir/main.swift"
  "$tenant_dir/AppDelegate.swift"
  "$tenant_dir/ClaudeStyle.swift"
  "$tenant_dir/StartupWindow.swift"
  "$tenant_dir/LocalCage.swift"
  "$tenant_dir/PortalAddress.swift"
  "$tenant_dir/PortalPolicy.swift"
  "$tenant_dir/PortalWindow.swift"
  "$tenant_dir/PortalWebWindow.swift"
)
frameworks=(-framework AppKit -framework WebKit -framework Security)
# The only entitlements the signed App may carry, as sorted "key=value" lines.
expected_entitlements=$'com.apple.security.app-sandbox=true\ncom.apple.security.network.client=true'

# Unset or "-" signs ad-hoc, which is only good for local testing.
identity="${CAGE_SIGNING_IDENTITY:--}"
[[ -n "$identity" ]] || identity=-

die() { printf '%s\n' "$*" >&2; exit 1; }

require_macos() {
  [[ "$(uname -s)" == Darwin ]] || die 'Cage Tenant must be built on macOS.'
}

# physical_path PATH: PATH with symlinks resolved. Missing trailing
# components are kept as written, so the folder need not exist yet.
physical_path() {
  local path="${1%/}" rest='' resolved
  [[ -n "$path" ]] || path=/
  while [[ ! -d "$path" ]]; do
    if [[ -e "$path" || -L "$path" ]]; then die "Not a folder: $path"; fi
    rest="/${path##*/}$rest"
    path="$(dirname "$path")"
  done
  resolved="$(cd "$path" && pwd -P)"
  resolved="${resolved%/}$rest"
  printf '%s\n' "${resolved:-/}"
}

# Every script writes only to output_dir: the App, the DMG, SHA256SUMS and
# staging folders that are removed on exit. The default is _output/tenant-macos.
# CAGE_TENANT_OUTPUT_DIR selects another absolute folder, for example a
# separate release folder, so a signed build never replaces a test build.
select_output_dir() {
  local requested="${CAGE_TENANT_OUTPUT_DIR:-}" resolved applications
  if [[ -z "$requested" ]]; then
    output_dir="$repo_dir/_output/tenant-macos"
    return
  fi
  [[ "$requested" == /* ]] || die 'CAGE_TENANT_OUTPUT_DIR must be an absolute path.'
  [[ "$requested" != *[[:cntrl:]]* ]] || die 'CAGE_TENANT_OUTPUT_DIR must not contain control characters.'
  [[ "/$requested/" != *"/./"* && "/$requested/" != *"/../"* ]] \
    || die 'CAGE_TENANT_OUTPUT_DIR must not contain . or .. components.'
  resolved="$(physical_path "$requested")"
  ! within "$repo_dir" "$resolved" \
    || die 'CAGE_TENANT_OUTPUT_DIR must not be the repository or a folder that contains it.'
  if within "$resolved" "$repo_dir" \
    && { [[ "$resolved" == "$repo_dir/_output" ]] || ! within "$resolved" "$repo_dir/_output"; }; then
    die "Inside the repository, CAGE_TENANT_OUTPUT_DIR must be a folder under $repo_dir/_output."
  fi
  [[ "$resolved" != "$(physical_path "$HOME")" ]] || die 'CAGE_TENANT_OUTPUT_DIR must not be the home folder.'
  for applications in /Applications "$HOME/Applications"; do
    ! within "$resolved" "$(physical_path "$applications")" \
      || die 'CAGE_TENANT_OUTPUT_DIR must not be in an Applications folder; these scripts never install.'
  done
  output_dir="$resolved"
}

# within PATH FOLDER: PATH is FOLDER or inside it (both physical paths).
within() { [[ "$2" == / || "$1/" == "$2/"* ]]; }
select_output_dir
app_path="$output_dir/$app_name.app"

# The output folder holds only this project's artifacts. Refuse to replace or
# sit beside an App or DMG that this project did not build, or a modified App.
check_output_artifacts() {
  local entry
  [[ -d "$output_dir" ]] || return 0
  for entry in "$output_dir"/*.app "$output_dir"/*.dmg "$output_dir/SHA256SUMS"; do
    [[ -e "$entry" || -L "$entry" ]] || continue
    case "$entry" in
      *.app) check_existing_app "$entry" ;;
      *.dmg) check_existing_dmg "$entry" ;;
      *) check_existing_sums "$entry" ;;
    esac
  done
}

check_existing_app() {
  local app="$1"
  if [[ "$app" != "$app_path" || -L "$app" || ! -d "$app" ]]; then
    die "$app was not built by this project. Choose another CAGE_TENANT_OUTPUT_DIR."
  fi
  [[ "$(plutil -extract CFBundleIdentifier raw -o - "$app/Contents/Info.plist" 2>/dev/null)" == "$bundle_id" ]] \
    || die "$app is not Cage Tenant; it was kept. Choose another CAGE_TENANT_OUTPUT_DIR."
  codesign --verify --deep --strict "$app" 2>/dev/null \
    || die "$app has been modified or cannot be verified; it was kept. Move it away or choose another CAGE_TENANT_OUTPUT_DIR."
  if [[ -n "$(/usr/sbin/lsof -t "$app/Contents/MacOS/$executable" 2>/dev/null || true)" ]]; then
    die "Cage Tenant is running from $app. Quit it before rebuilding."
  fi
}

check_existing_dmg() {
  local dmg="$1"
  if [[ -L "$dmg" || ! -f "$dmg" || ! "${dmg##*/}" =~ ^Cage-Tenant-[A-Za-z0-9.-]+-macos-universal\.dmg$ ]]; then
    die "$dmg is not a Cage Tenant DMG. Choose another CAGE_TENANT_OUTPUT_DIR."
  fi
  if ! codesign --verify --strict "$dmg" 2>/dev/null \
    || ! grep -qx "Identifier=$dmg_identifier" <<<"$(codesign --display --verbose=2 "$dmg" 2>&1)"; then
    die "$dmg is not a verified Cage Tenant DMG; it was kept. Choose another CAGE_TENANT_OUTPUT_DIR."
  fi
}

check_existing_sums() {
  local sums="$1"
  if [[ -L "$sums" || ! -f "$sums" ]] \
    || grep -Evq '^[0-9a-f]{64}  Cage-Tenant-[A-Za-z0-9.-]+-macos-universal\.dmg$' "$sums"; then
    die "$sums lists files other than Cage Tenant DMGs. Choose another CAGE_TENANT_OUTPUT_DIR."
  fi
}

# Sets version (from the repository VERSION file), short_version and build_number.
read_version() {
  version="$(tr -d '\n\r' < "$repo_dir/VERSION")"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || die "Invalid VERSION: $version"
  short_version="${version%%-*}"
  build_number="${CAGE_BUILD_NUMBER:-1}"
  [[ "$build_number" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]] || die "Invalid CAGE_BUILD_NUMBER: $build_number"
}

# Real identities get a secure timestamp. Ad-hoc signatures cannot carry one.
signing_options() {
  if [[ "$identity" != - ]]; then printf '%s\n' --timestamp; fi
}

# verify_signature PATH: strict verification plus the properties this
# identity must produce (ad-hoc, or a secure timestamp from a real identity).
verify_signature() {
  local target="$1" details
  codesign --verify --strict --verbose=2 "$target"
  details="$(codesign --display --verbose=4 "$target" 2>&1)"
  if [[ "$identity" == - ]]; then
    grep -qx 'Signature=adhoc' <<<"$details" || die "Expected an ad-hoc signature on $target."
  else
    grep -q '^Timestamp=' <<<"$details" || die "Missing secure timestamp on $target."
    if ! grep -q '^Authority=Developer ID Application: ' <<<"$details"; then
      printf 'Warning: %s is not signed with a Developer ID Application certificate; it is not suitable for distribution.\n' "$target" >&2
    fi
  fi
}

# entitlement_lines PLIST: entitlements as sorted "key=value" lines. Nested
# or non-boolean values print as something other than true/false, so they
# never match the expected list.
entitlement_lines() {
  local plist="$1" key
  plutil -convert xml1 -o - "$plist" | sed -n 's:^[[:space:]]*<key>\(.*\)</key>[[:space:]]*$:\1:p' | sort -u |
    while IFS= read -r key; do
      printf '%s=%s\n' "$key" "$(/usr/libexec/PlistBuddy -c "Print :$key" "$plist" 2>/dev/null || echo '<missing>')"
    done
}

# verify_app PATH: the signed App has the expected identity, the hardened
# runtime, both architectures and only the expected entitlements.
verify_app() {
  local app="$1" details archs entitlements scratch
  verify_signature "$app"
  codesign --verify --deep --strict "$app"
  details="$(codesign --display --verbose=4 "$app" 2>&1)"
  grep -qx "Identifier=$bundle_id" <<<"$details" || die "Unexpected signing identifier on $app."
  grep -Eq '^CodeDirectory .*flags=0x[0-9a-f]+\([^)]*runtime' <<<"$details" || die "Hardened runtime is not enabled on $app."
  [[ "$(plutil -extract CFBundleIdentifier raw -o - "$app/Contents/Info.plist")" == "$bundle_id" ]] || die 'Unexpected bundle identifier.'
  archs="$(lipo -archs "$app/Contents/MacOS/$executable")"
  for arch in "${architectures[@]}"; do
    [[ " $archs " == *" $arch "* ]] || die "Missing $arch slice in $app."
  done
  [[ "$(wc -w <<<"$archs" | tr -d ' ')" == "${#architectures[@]}" ]] || die "Unexpected slices in $app: $archs"
  mkdir -p "$output_dir"
  scratch="$(mktemp "$output_dir/.entitlements.XXXXXX")"
  codesign --display --entitlements - --xml "$app" 2>/dev/null > "$scratch"
  entitlements="$(entitlement_lines "$scratch")"
  rm -f "$scratch"
  [[ "$entitlements" == "$expected_entitlements" ]] || die "Unexpected entitlements on $app: $entitlements"
}

# verify_dmg PATH: signed by this identity with the Cage Tenant DMG identifier.
verify_dmg() {
  verify_signature "$1"
  grep -qx "Identifier=$dmg_identifier" <<<"$(codesign --display --verbose=2 "$1" 2>&1)" \
    || die "Unexpected signing identifier on $1."
}
