#!/usr/bin/env bash
# Build cage-tenant-desktop_<version>_all.deb in the output folder (default
# _output/tenant-linux) and write SHA256SUMS. Uses dpkg-deb when installed,
# otherwise tools/build_deb.py. Does not install, sign or publish anything.
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant-linux/build-deb.sh'
read_version
check_output_artifacts
mkdir -p "$output_dir"
stage="$(mktemp -d "$output_dir/.stage.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
umask 022
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$repo_dir" log -1 --format=%ct 2>/dev/null || echo 0)}"

root="$stage/root"
share="$root/usr/share"
module="$share/$package/cage_tenant_desktop"
doc="$share/doc/$package"
install -d "$root/DEBIAN" "$root/usr/bin" "$module" "$share/applications" "$doc"
install -m 0755 "$linux_dir/cage-tenant-desktop" "$root/usr/bin/$package"
for source in "$linux_dir"/cage_tenant_desktop/*.py; do
  install -m 0644 "$source" "$module/"
done
printf '%s\n' "$version" > "$module/VERSION"
install -m 0644 "$linux_dir/$app_id.desktop" "$share/applications/$app_id.desktop"
for size in "${icon_sizes[@]}"; do
  install -d "$share/icons/hicolor/${size}x${size}/apps"
  resize_png "$size" "$share/icons/hicolor/${size}x${size}/apps/$app_id.png"
  chmod 0644 "$share/icons/hicolor/${size}x${size}/apps/$app_id.png"
done
install -m 0644 "$linux_dir/debian/copyright" "$doc/copyright"
changelog_date="$(python3 -I -c 'import email.utils,sys; print(email.utils.formatdate(int(sys.argv[1]), usegmt=False))' "$SOURCE_DATE_EPOCH")"
printf '%s (%s) unstable; urgency=medium\n\n  * Local build of the Cage Tenant Linux client.\n\n -- Cage contributors <noreply@github.com>  %s\n' \
  "$package" "$version" "$changelog_date" | gzip -9n > "$doc/changelog.Debian.gz"
chmod 0644 "$doc/changelog.Debian.gz"

installed_size="$(du -sk "$root/usr" | cut -f1)"
sed -e "s/@VERSION@/$version/" -e "s/@INSTALLED_SIZE@/$installed_size/" \
  "$linux_dir/debian/control.in" > "$root/DEBIAN/control"
(cd "$root" && find usr -type f | LC_ALL=C sort | while IFS= read -r file; do
  if command -v md5sum >/dev/null; then md5sum "$file"; else printf '%s  %s\n' "$(md5 -q "$file")" "$file"; fi
done) > "$root/DEBIAN/md5sums"
chmod 0644 "$root/DEBIAN/control" "$root/DEBIAN/md5sums"
find "$root" -type d -exec chmod 0755 {} +

deb="$output_dir/$deb_name"
rm -f "$deb"
if command -v dpkg-deb >/dev/null; then
  dpkg-deb --root-owner-group -Zxz --build "$root" "$deb" >/dev/null
  builder=dpkg-deb
else
  python3 -I "$linux_dir/tools/build_deb.py" build "$root" "$deb"
  builder=tools/build_deb.py
fi
(cd "$output_dir" && sha256 "$deb_name" > SHA256SUMS && sha256 -c SHA256SUMS >/dev/null)
printf 'Built %s with %s\n' "$deb" "$builder"
