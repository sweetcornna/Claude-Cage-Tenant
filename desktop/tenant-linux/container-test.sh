#!/usr/bin/env bash
# Build, install and drive the Linux client in a disposable container.
# Needs Docker (or a compatible CLI). Changes nothing on this machine except
# the test image tag cage-tenant-linux-test:<base> and <output>/container/.
# CAGE_TENANT_LINUX_TEST_BASE selects the base image (default ubuntu:24.04).
set -euo pipefail
# shellcheck source=common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
[[ $# -eq 0 ]] || die 'Usage: desktop/tenant-linux/container-test.sh'
command -v docker >/dev/null || die 'Docker is required for the container test.'
base="${CAGE_TENANT_LINUX_TEST_BASE:-ubuntu:24.04}"
[[ "$base" =~ ^(ubuntu:(22\.04|24\.04)|debian:(12|13))$ ]] || die "Unsupported base image: $base"
image="cage-tenant-linux-test:${base/:/-}"
read_version
"$linux_dir/build-deb.sh"
results="$output_dir/container/${base/:/-}"
rm -rf "$results"
mkdir -p "$results"
docker build -q -t "$image" --build-arg "BASE=$base" -f "$linux_dir/tests/Containerfile" "$linux_dir/tests" >/dev/null
docker run --rm \
  -v "$repo_dir:/src:ro" \
  -v "$output_dir:/host-deb:ro" \
  -v "$results:/out" \
  -e SOURCE_DATE_EPOCH="$(git -C "$repo_dir" log -1 --format=%ct 2>/dev/null || echo 0)" \
  "$image" bash /src/desktop/tenant-linux/tests/in-container.sh 2>&1 | tee "$results/log.txt"
printf '\nResults and screenshots: %s\n' "$results"
