#!/usr/bin/env bash
# Publish Cage Tenant for Windows x64 (self-contained single file) and pack
# Cage-Tenant-<version>-windows-x64-unsigned.zip plus SHA256SUMS in the output
# folder (default _output/tenant-windows). Runs on macOS, Linux or Windows
# (Git Bash) with the .NET 10 SDK. Does not sign, install or publish.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant-windows/build.sh'
require_dotnet
read_version
check_output_artifacts
mkdir -p "$output_dir"
publish="$output_dir/.publish"
stage="$(mktemp -d "$output_dir/.stage.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
rm -rf "$publish"

dotnet_here publish "$project" -c Release -nologo -v quiet "-p:Version=$version" -o "$publish"
exe="$publish/CageTenant.exe"
[[ -f "$exe" ]] || die "Publish did not produce $exe."
python3 -I "$windows_dir/tools/pe_check.py" "$exe"

folder="$stage/Cage Tenant"
mkdir -p "$folder"
cp "$exe" "$folder/CageTenant.exe"
python3 -I - "$windows_dir/zip-readme.txt" "$folder/README.txt" "$version" <<'PY'
import sys
text = open(sys.argv[1], encoding="utf-8").read().replace("@VERSION@", sys.argv[3])
open(sys.argv[2], "w", encoding="utf-8-sig", newline="\r\n").write(text)
PY
python3 -I - "$windows_dir/LICENSE" "$folder/LICENSE.txt" <<'PY'
import sys
open(sys.argv[2], "w", encoding="utf-8", newline="\r\n").write(open(sys.argv[1], encoding="utf-8").read())
PY

zip="$output_dir/$zip_name"
epoch="${SOURCE_DATE_EPOCH:-$(git -C "$repo_dir" log -1 --format=%ct 2>/dev/null || echo 315532800)}"
python3 -I - "$stage" "$zip" "$epoch" <<'PY'
import os, sys, time, zipfile
stage, out, epoch = sys.argv[1], sys.argv[2], max(int(sys.argv[3]), 315532800)
stamp = time.gmtime(epoch)[:6]
tmp = out + ".tmp"
with zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for name in ["Cage Tenant/", "Cage Tenant/CageTenant.exe", "Cage Tenant/LICENSE.txt", "Cage Tenant/README.txt"]:
        info = zipfile.ZipInfo(name, stamp)
        info.create_system = 0
        if name.endswith("/"):
            info.external_attr = 0x10
            z.writestr(info, b"")
        else:
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(os.path.join(stage, name), "rb") as f:
                z.writestr(info, f.read())
os.replace(tmp, out)
PY
(cd "$output_dir" && sha256 "$zip_name" > SHA256SUMS && sha256 -c SHA256SUMS >/dev/null)
python3 -I -c 'import sys, zipfile; [print(f"{i.file_size:>10}  {i.filename}") for i in zipfile.ZipFile(sys.argv[1]).infolist()]' "$zip"
printf 'Built %s (unsigned)\n' "$zip"
