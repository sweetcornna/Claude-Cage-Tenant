"""Report and check the PE header of the published CageTenant.exe.

Usage: python3 -I pe_check.py <CageTenant.exe>

Checks: PE32+ for x64 (machine 0x8664), Windows GUI subsystem, a .NET
single-file bundle marker, and an embedded application manifest that asks
for asInvoker. Prints whether an Authenticode signature is present; local
builds are unsigned.
"""

from __future__ import annotations

import struct
import sys

# The .NET single-file host looks for this SHA-256 (of ".net core bundle")
# to find the bundle header.
BUNDLE_MARKER = bytes([
    0x8B, 0x12, 0x02, 0xB9, 0x6A, 0x61, 0x20, 0x38, 0x72, 0x7B, 0x93, 0x02, 0x14, 0xD7, 0xA0, 0x32,
    0x13, 0xF5, 0xB9, 0xE6, 0xEF, 0xAE, 0x33, 0x18, 0xEE, 0x3B, 0x2D, 0xCE, 0x24, 0xB3, 0x6A, 0xAE,
])


def main(argv) -> int:
    if len(argv) != 2:
        sys.stderr.write(__doc__)
        return 2
    data = open(argv[1], "rb").read()
    problems = []
    if data[:2] != b"MZ":
        print("not a PE file")
        return 1
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe:pe + 4] != b"PE\0\0":
        print("missing PE signature")
        return 1
    machine = struct.unpack_from("<H", data, pe + 4)[0]
    optional = pe + 24
    magic = struct.unpack_from("<H", data, optional)[0]
    subsystem = struct.unpack_from("<H", data, optional + 68)[0]
    # PE32+ data directories start at optional + 112; entry 4 is the certificate table.
    cert_rva, cert_size = struct.unpack_from("<II", data, optional + 112 + 4 * 8)
    print(f"machine=0x{machine:04x} magic=0x{magic:03x} subsystem={subsystem} size={len(data)}")
    if machine != 0x8664:
        problems.append("not an x64 executable")
    if magic != 0x20B:
        problems.append("not PE32+")
    if subsystem != 2:
        problems.append("not a Windows GUI program")
    if BUNDLE_MARKER not in data:
        problems.append("no .NET single-file bundle marker")
    if b"asInvoker" not in data or b"PerMonitorV2" not in data:
        problems.append("application manifest (asInvoker, PerMonitorV2) not found")
    print("authenticode=" + ("present" if cert_size else "none (unsigned)"))
    for problem in problems:
        print("FAIL: " + problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
