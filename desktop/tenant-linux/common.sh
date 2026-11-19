# Shared settings for the Cage Tenant Linux scripts. Source this file; do not run it.
# shellcheck shell=bash
# shellcheck disable=SC2034  # variables are used by the scripts that source this file

linux_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_dir="$(cd "$linux_dir/../.." && pwd -P)"
package='cage-tenant-desktop'
app_id='io.github.sweetcornna.claude-cage.tenant'
swift_vectors="$repo_dir/desktop/tenant/Tests/TenantTests.swift"
icon_sizes=(48 64 128 256)

die() { printf '%s\n' "$*" >&2; exit 1; }

# physical_path PATH: PATH with symlinks resolved; missing trailing components kept.
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

# within PATH FOLDER: PATH is FOLDER or inside it (both physical paths).
within() { [[ "$2" == / || "$1/" == "$2/"* ]]; }

# Scripts write only to output_dir (default _output/tenant-linux). The
# package, SHA256SUMS, container-test results and staging folders that are
# removed on exit. CAGE_TENANT_LINUX_OUTPUT_DIR selects another absolute folder.
select_output_dir() {
  local requested="${CAGE_TENANT_LINUX_OUTPUT_DIR:-}" resolved
  if [[ -z "$requested" ]]; then
    output_dir="$repo_dir/_output/tenant-linux"
    return
  fi
  [[ "$requested" == /* ]] || die 'CAGE_TENANT_LINUX_OUTPUT_DIR must be an absolute path.'
  [[ "$requested" != *[[:cntrl:]]* ]] || die 'CAGE_TENANT_LINUX_OUTPUT_DIR must not contain control characters.'
  [[ "/$requested/" != *"/./"* && "/$requested/" != *"/../"* ]] \
    || die 'CAGE_TENANT_LINUX_OUTPUT_DIR must not contain . or .. components.'
  resolved="$(physical_path "$requested")"
  ! within "$repo_dir" "$resolved" \
    || die 'CAGE_TENANT_LINUX_OUTPUT_DIR must not be the repository or a folder that contains it.'
  if within "$resolved" "$repo_dir" \
    && { [[ "$resolved" == "$repo_dir/_output" ]] || ! within "$resolved" "$repo_dir/_output"; }; then
    die "Inside the repository, CAGE_TENANT_LINUX_OUTPUT_DIR must be a folder under $repo_dir/_output."
  fi
  [[ "$resolved" != "$(physical_path "$HOME")" ]] || die 'CAGE_TENANT_LINUX_OUTPUT_DIR must not be the home folder.'
  for system in /usr /etc /opt /var/lib/dpkg; do
    ! within "$resolved" "$system" || die "CAGE_TENANT_LINUX_OUTPUT_DIR must not be under $system; these scripts never install."
  done
  output_dir="$resolved"
}
select_output_dir

# The output folder holds only this project's artifacts. Refuse to replace or
# sit beside a package or checksum list this project did not write.
check_output_artifacts() {
  local entry
  [[ -d "$output_dir" ]] || return 0
  for entry in "$output_dir"/*.deb "$output_dir/SHA256SUMS"; do
    [[ -e "$entry" || -L "$entry" ]] || continue
    case "$entry" in
      *.deb)
        if [[ -L "$entry" || ! -f "$entry" || ! "${entry##*/}" =~ ^${package}_[A-Za-z0-9.~+-]+_all\.deb$ ]]; then
          die "$entry is not a $package package; it was kept. Choose another CAGE_TENANT_LINUX_OUTPUT_DIR."
        fi ;;
      *)
        if [[ -L "$entry" || ! -f "$entry" ]] || grep -Evq "^[0-9a-f]{64}  ${package}_[A-Za-z0-9.~+-]+_all\.deb$" "$entry"; then
          die "$entry lists files other than $package packages; it was kept. Choose another CAGE_TENANT_LINUX_OUTPUT_DIR."
        fi ;;
    esac
  done
}

# Sets version (Debian form of the repository VERSION: 0.2.0-rc.1 -> 0.2.0~rc.1).
read_version() {
  local raw
  raw="$(tr -d '\n\r' < "$repo_dir/VERSION")"
  [[ "$raw" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$ ]] || die "Invalid VERSION: $raw"
  version="${raw/-/\~}"
  deb_name="${package}_${version}_all.deb"
}

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

# resize_png SIZE OUT: logo.png scaled to SIZE x SIZE.
resize_png() {
  local size="$1" out="$2"
  if command -v sips >/dev/null; then
    sips -s format png -z "$size" "$size" "$repo_dir/logo.png" --out "$out" >/dev/null
  elif command -v magick >/dev/null; then
    magick "$repo_dir/logo.png" -strip -resize "${size}x${size}" "$out"
  elif command -v convert >/dev/null; then
    convert "$repo_dir/logo.png" -strip -resize "${size}x${size}" "$out"
  else
    die 'Install ImageMagick (or use macOS sips) to scale the icon.'
  fi
}
