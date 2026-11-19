#!/usr/bin/env bash
# Check Cage Tenant for Windows without Windows: cross-platform logic tests
# (address vectors shared with the macOS client, store, navigation policy),
# a warnings-as-errors build of the WinForms/WebView2 app for win-x64,
# source checks for the WebView2 security settings, and script checks.
# Writes only to the output folder (default _output/tenant-windows).
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant-windows/test.sh'
require_dotnet
[[ -f "$swift_vectors" ]] || die "Missing shared address vectors: $swift_vectors"
mkdir -p "$output_dir"

dotnet_here build "$tests_project" -c Release -nologo -v quiet -clp:NoSummary
printf 'Logic tests: '
dotnet "$output_dir/.build/bin/CageTenant.Windows.Tests/release/CageTenant.Windows.Tests.dll" "$swift_vectors"

dotnet_here build "$project" -c Release -nologo -v quiet -clp:NoSummary -p:Version=0.0.0
printf 'WinForms + WebView2 app (win-x64): build passed with warnings as errors\n'

# The window must not add a native bridge, inject script, read cookies,
# bypass TLS, open other programs, or filter subframes with the main-frame rule.
cd "$windows_dir"
launcher_forbidden='AddHostObjectToScript|AddScriptToExecuteOnDocumentCreated|ExecuteScriptAsync|PostWebMessage|WebMessageReceived|CookieManager|GetCookiesAsync|AddWebResourceRequestedFilter|WebResourceRequested|ServerCertificateErrorAction\.AlwaysAllow|ignore-certificate|OpenDevToolsWindow|FrameNavigationStarting|BasicAuthenticationRequested'
forbidden="$launcher_forbidden|Process\.Start|ShellExecute"
# Only the start page's local option may start a program: the installed
# upstream Cage or its pinned, SHA-256 verified installer.
for source in "${app_sources[@]}"; do
  pattern="$forbidden"
  [[ "$source" == LocalCageLauncher.cs ]] && pattern="$launcher_forbidden"
  # Comments may name these APIs; code may not.
  if sed -E 's#//.*$##' "$source" | grep -nE "$pattern"; then
    die "$source uses an API that could add a bridge, read cookies, bypass TLS or open another program."
  fi
done
for pinned in 'Convert.ToHexStringLower(SHA256.HashData(stream)) == sha256' \
  'Verify(string path) => Verify(path, Size, Sha256);' \
  'PrepareAsync(folder, Size, Sha256, IdleTimeout, handler, cancel);'; do
  grep -qF "$pinned" LocalInstaller.cs || die 'The local installer must be verified against its pinned SHA-256.'
done
for required in 'AreHostObjectsAllowed = false' 'IsWebMessageEnabled = false' 'AreDevToolsEnabled = false' \
  'IsPasswordAutosaveEnabled = false' 'IsGeneralAutofillEnabled = false' 'e.Handled = true' \
  'CoreWebView2ServerCertificateErrorAction.Cancel' 'AllowSingleSignOnUsingOSPrimaryAccount = false' \
  'AreBrowserExtensionsEnabled = false' 'WebPolicy.IsPortalDocument(e.Uri, target)' 'ClearBrowsingDataAsync' \
  'store.RemoveSessionsExcept(origin)' 'WebView2VariablesToClear'; do
  grep -qF -- "$required" MainForm.cs Program.cs || die "Missing expected setting: $required"
done
grep -q '^MIT License$' LICENSE || die 'LICENSE is not the MIT license.'
grep -q 'Include="Microsoft.Web.WebView2" Version="1.0.4314.35"' CageTenant.Windows.csproj || die 'Unexpected WebView2 SDK version.'
printf 'WebView2 security settings: passed\n'

for script in common.sh build.sh test.sh; do bash -n "$script"; done
if command -v shellcheck >/dev/null; then shellcheck -x common.sh build.sh test.sh; fi
python3 -I -m py_compile tools/pe_check.py && rm -rf tools/__pycache__

# Output folder selection: only folders under _output (or outside the repository).
outcome() {
  if CAGE_TENANT_WINDOWS_OUTPUT_DIR="$1" bash -c 'source "$1/common.sh"' _ "$windows_dir" >/dev/null 2>&1; then echo ok; else echo fail; fi
}
[[ "$(outcome "$repo_dir/_output/tenant-windows-release")" == ok ]] || die 'A folder under _output must be accepted.'
for bad in relative/path "$repo_dir" "$repo_dir/_output" "$repo_dir/desktop" "$repo_dir/_output/../x" "$HOME" /; do
  [[ "$(outcome "$bad")" == fail ]] || die "Output folder must be refused: $bad"
done
printf 'Scripts and output folder checks: passed\n'
