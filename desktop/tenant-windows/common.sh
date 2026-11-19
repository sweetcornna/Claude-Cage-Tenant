# Shared settings for the Cage Tenant Windows scripts. Source this file; do not run it.
# shellcheck shell=bash
# shellcheck disable=SC2034  # variables are used by the scripts that source this file

windows_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_dir="$(cd "$windows_dir/../.." && pwd -P)"
project="$windows_dir/CageTenant.Windows.csproj"
tests_project="$windows_dir/Tests/CageTenant.Windows.Tests.csproj"
swift_vectors="$repo_dir/desktop/tenant/Tests/TenantTests.swift"
app_sources=(Program.cs MainForm.cs ClaudeTheme.cs LocalInstaller.cs LocalCageLauncher.cs PortalAddress.cs PortalStore.cs WebPolicy.cs)
export DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1

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

# Scripts write only to output_dir (default _output/tenant-windows): the ZIP,
# SHA256SUMS, and the .build, .publish and staging folders. MSBuild's bin/obj
# go to <output>/.build, never into the source tree.
# CAGE_TENANT_WINDOWS_OUTPUT_DIR selects another absolute folder.
select_output_dir() {
  local requested="${CAGE_TENANT_WINDOWS_OUTPUT_DIR:-}" resolved
  if [[ -z "$requested" ]]; then
    output_dir="$repo_dir/_output/tenant-windows"
    return
  fi
  [[ "$requested" == /* ]] || die 'CAGE_TENANT_WINDOWS_OUTPUT_DIR must be an absolute path.'
  [[ "$requested" != *[[:cntrl:]]* ]] || die 'CAGE_TENANT_WINDOWS_OUTPUT_DIR must not contain control characters.'
  [[ "/$requested/" != *"/./"* && "/$requested/" != *"/../"* ]] \
    || die 'CAGE_TENANT_WINDOWS_OUTPUT_DIR must not contain . or .. components.'
  resolved="$(physical_path "$requested")"
  ! within "$repo_dir" "$resolved" \
    || die 'CAGE_TENANT_WINDOWS_OUTPUT_DIR must not be the repository or a folder that contains it.'
  if within "$resolved" "$repo_dir" \
    && { [[ "$resolved" == "$repo_dir/_output" ]] || ! within "$resolved" "$repo_dir/_output"; }; then
    die "Inside the repository, CAGE_TENANT_WINDOWS_OUTPUT_DIR must be a folder under $repo_dir/_output."
  fi
  [[ "$resolved" != "$(physical_path "$HOME")" ]] || die 'CAGE_TENANT_WINDOWS_OUTPUT_DIR must not be the home folder.'
  output_dir="$resolved"
}
select_output_dir

# The output folder holds only this project's artifacts. Refuse to replace or
# sit beside a ZIP or checksum list this project did not write.
check_output_artifacts() {
  local entry
  [[ -d "$output_dir" ]] || return 0
  for entry in "$output_dir"/*.zip "$output_dir/SHA256SUMS"; do
    [[ -e "$entry" || -L "$entry" ]] || continue
    case "$entry" in
      *.zip)
        if [[ -L "$entry" || ! -f "$entry" || ! "${entry##*/}" =~ ^Cage-Tenant-[A-Za-z0-9.-]+-windows-x64-unsigned\.zip$ ]]; then
          die "$entry is not a Cage Tenant Windows ZIP; it was kept. Choose another CAGE_TENANT_WINDOWS_OUTPUT_DIR."
        fi ;;
      *)
        if [[ -L "$entry" || ! -f "$entry" ]] \
          || grep -Evq '^[0-9a-f]{64}  Cage-Tenant-[A-Za-z0-9.-]+-windows-x64-unsigned\.zip$' "$entry"; then
          die "$entry lists files other than Cage Tenant Windows ZIPs; it was kept. Choose another CAGE_TENANT_WINDOWS_OUTPUT_DIR."
        fi ;;
    esac
  done
}

# Sets version from the repository VERSION file.
read_version() {
  version="$(tr -d '\n\r' < "$repo_dir/VERSION")"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || die "Invalid VERSION: $version"
  zip_name="Cage-Tenant-$version-windows-x64-unsigned.zip"
}

require_dotnet() {
  command -v dotnet >/dev/null || die 'The .NET SDK 10 is required (https://dot.net).'
  (cd "$windows_dir" && dotnet --version >/dev/null 2>&1) \
    || die 'No .NET SDK 10.0.100 or newer 10.0 SDK was found (see desktop/tenant-windows/global.json).'
}

# dotnet resolves global.json from the working folder, so run it from here.
dotnet_here() { (cd "$windows_dir" && dotnet "$@" "-p:ArtifactsPath=$output_dir/.build"); }

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}
