#!/usr/bin/env bash
# Runs inside the disposable test container started by container-test.sh.
# /src is the repository (read-only), /out receives results, /host-deb holds
# the package built on the host. Changes the container only.
set -euo pipefail
step() { printf '\n== %s\n' "$*"; }
out=/out
work=/work/repo
mkdir -p "$out" "$work/desktop/tenant/Tests"
cp -a /src/desktop/tenant-linux "$work/desktop/"
cp /src/desktop/tenant/Tests/TenantTests.swift "$work/desktop/tenant/Tests/"
cp /src/VERSION /src/logo.png "$work/"
cd "$work"
rm -rf desktop/tenant-linux/cage_tenant_desktop/__pycache__ desktop/tenant-linux/tests/__pycache__
export PYTHONDONTWRITEBYTECODE=1

step "Unit tests ($(python3 --version))"
python3 -I desktop/tenant-linux/tests/test_client.py desktop/tenant/Tests/TenantTests.swift

step 'Build with dpkg-deb'
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-0}" desktop/tenant-linux/build-deb.sh
deb="$(echo _output/tenant-linux/cage-tenant-desktop_*_all.deb)"
dpkg-deb --info "$deb"
dpkg-deb --contents "$deb" | awk '{print $1, $2, $6}' | tee "$out/contents-dpkg-deb.txt"
cp "$deb" "$out/"

host_deb="$(echo /host-deb/cage-tenant-desktop_*_all.deb)"
if [[ -f "$host_deb" ]]; then
  step "Package built on the host (tools/build_deb.py): $(basename "$host_deb")"
  dpkg-deb --info "$host_deb" >/dev/null
  dpkg-deb --contents "$host_deb" | awk '{print $1, $2, $6}' > "$out/contents-host.txt"
  diff -u "$out/contents-dpkg-deb.txt" "$out/contents-host.txt"
  diff -u <(dpkg-deb --field "$deb" | grep -v '^Installed-Size') <(dpkg-deb --field "$host_deb" | grep -v '^Installed-Size')
  echo 'same files, modes, owners and control fields as the dpkg-deb build'
fi

step 'lintian'
lintian --fail-on error --info "$deb" | tee "$out/lintian.txt" || { echo 'lintian reported errors'; exit 1; }

step 'Install with apt (dependencies from the archive)'
apt-get update -qq
before="$(dpkg-query -W -f '${Package}\n' | sort)"
apt-get install -y -qq --no-install-recommends "./$deb" >/dev/null
comm -13 <(echo "$before") <(dpkg-query -W -f '${Package}\n' | sort) > "$out/installed-packages.txt"
printf '%s packages installed, including: %s\n' "$(wc -l < "$out/installed-packages.txt")" \
  "$(grep -E '^(cage-tenant-desktop|gir1.2-webkit2-4.1|libwebkit2gtk-4.1-0|python3-gi|gir1.2-gtk-3.0)$' "$out/installed-packages.txt" | tr '\n' ' ')"
dpkg-query -W -f '${Package} ${Version}\n' cage-tenant-desktop gir1.2-webkit2-4.1 libwebkit2gtk-4.1-0 python3-gi gir1.2-gtk-3.0 | tee "$out/versions.txt"
desktop-file-validate /usr/share/applications/io.github.sweetcornna.claude-cage.tenant.desktop && echo 'desktop-file-validate: ok'
cage-tenant-desktop --version
useradd -m tester

step 'GUI smoke test under Xvfb'
python3 -I desktop/tenant-linux/tests/gui_smoke.py "$out/gui"

step 'Remove'
apt-get remove -y -qq cage-tenant-desktop >/dev/null
for path in /usr/bin/cage-tenant-desktop /usr/share/cage-tenant-desktop /usr/share/applications/io.github.sweetcornna.claude-cage.tenant.desktop; do
  [[ ! -e "$path" ]] || { echo "left behind: $path"; exit 1; }
done
echo 'package files removed'
