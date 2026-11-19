"""Write or inspect a Debian binary package without dpkg-deb.

Usage:
  python3 -I build_deb.py build <staged root with DEBIAN/> <output .deb>
  python3 -I build_deb.py list <.deb>

build.sh uses dpkg-deb when it is installed (Debian/Ubuntu); this writer is
the fallback for other build hosts (for example macOS). It produces the
same layout dpkg-deb does: an ar archive with debian-binary,
control.tar.gz and data.tar.xz, every entry owned by root:root, with
mtimes set to SOURCE_DATE_EPOCH.
"""

from __future__ import annotations

import gzip
import io
import lzma
import os
import stat
import sys
import tarfile


def _epoch() -> int:
    return int(os.environ.get("SOURCE_DATE_EPOCH", "0"))


def _tar(root: str, members, compress) -> bytes:
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.GNU_FORMAT) as tar:
        for rel in members:
            path = os.path.join(root, rel) if rel != "." else root
            info = os.lstat(path)
            entry = tarfile.TarInfo("./" + rel if rel != "." else "./")
            if stat.S_ISDIR(info.st_mode):
                entry.type = tarfile.DIRTYPE
                entry.name = entry.name.rstrip("/") + "/"
            elif stat.S_ISREG(info.st_mode):
                entry.type = tarfile.REGTYPE
                entry.size = info.st_size
            else:
                raise SystemExit(f"unsupported file type: {path}")
            entry.mode = stat.S_IMODE(info.st_mode)
            entry.uid = entry.gid = 0
            entry.uname = entry.gname = "root"
            entry.mtime = _epoch()
            if entry.type == tarfile.REGTYPE:
                with open(path, "rb") as handle:
                    tar.addfile(entry, handle)
            else:
                tar.addfile(entry)
    return compress(raw.getvalue())


def _walk(root: str):
    out = ["."]
    for base, dirs, files in os.walk(root):
        dirs.sort()
        rel_base = os.path.relpath(base, root)
        for name in dirs + sorted(files):
            rel = os.path.normpath(os.path.join(rel_base, name))
            out.append(rel)
    return sorted(set(out), key=lambda p: (p != ".", p))


def _ar_member(name: str, data: bytes) -> bytes:
    header = (
        name.ljust(16)
        + str(_epoch()).ljust(12)
        + "0".ljust(6)
        + "0".ljust(6)
        + "100644".ljust(8)
        + str(len(data)).ljust(10)
        + "`\n"
    ).encode("ascii")
    assert len(header) == 60
    return header + data + (b"\n" if len(data) % 2 else b"")


def build(root: str, output: str) -> None:
    control_dir = os.path.join(root, "DEBIAN")
    if not os.path.isfile(os.path.join(control_dir, "control")):
        raise SystemExit("missing DEBIAN/control")
    data_members = [m for m in _walk(root) if m != "DEBIAN" and not m.startswith("DEBIAN" + os.sep)]
    control_members = _walk(control_dir)
    control = _tar(control_dir, control_members, lambda b: gzip.compress(b, 9, mtime=_epoch()))
    data = _tar(root, data_members, lambda b: lzma.compress(b, preset=6))
    temp = output + ".tmp"
    with open(temp, "wb") as handle:
        handle.write(b"!<arch>\n")
        handle.write(_ar_member("debian-binary", b"2.0\n"))
        handle.write(_ar_member("control.tar.gz", control))
        handle.write(_ar_member("data.tar.xz", data))
    os.replace(temp, output)


def read(deb: str):
    """Return {member name: bytes} for the three members, checking the format."""
    with open(deb, "rb") as handle:
        blob = handle.read()
    if not blob.startswith(b"!<arch>\n"):
        raise SystemExit("not an ar archive")
    pos, members = 8, {}
    while pos < len(blob):
        header = blob[pos : pos + 60]
        if len(header) != 60 or header[58:60] != b"`\n":
            raise SystemExit("bad ar header")
        name = header[:16].decode("ascii").strip().rstrip("/")
        size = int(header[48:58].decode("ascii").strip())
        members[name] = blob[pos + 60 : pos + 60 + size]
        pos += 60 + size + (size % 2)
    if list(members)[:1] != ["debian-binary"] or members["debian-binary"] != b"2.0\n":
        raise SystemExit("debian-binary must come first and be 2.0")
    return members


def _open_tar(name: str, data: bytes) -> tarfile.TarFile:
    if name.endswith(".xz"):
        data = lzma.decompress(data)
    elif name.endswith(".gz"):
        data = gzip.decompress(data)
    elif name.endswith(".zst"):
        raise SystemExit("zstd members need dpkg-deb to inspect")
    return tarfile.open(fileobj=io.BytesIO(data))


def listing(deb: str) -> None:
    """Print 'mode owner/group path' for each data entry, then the control file."""
    members = read(deb)
    data_name = next(n for n in members if n.startswith("data.tar"))
    control_name = next(n for n in members if n.startswith("control.tar"))
    with _open_tar(data_name, members[data_name]) as tar:
        for entry in tar.getmembers():
            print(f"{stat.filemode(entry.mode | (stat.S_IFDIR if entry.isdir() else stat.S_IFREG))} "
                  f"{entry.uname or entry.uid}/{entry.gname or entry.gid} {entry.name}")
    with _open_tar(control_name, members[control_name]) as tar:
        control = tar.extractfile("./control").read().decode("utf-8")
    print("--- control")
    print(control, end="")


def main(argv) -> int:
    if len(argv) == 4 and argv[1] == "build":
        build(argv[2], argv[3])
        return 0
    if len(argv) == 3 and argv[1] == "list":
        listing(argv[2])
        return 0
    sys.stderr.write(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
