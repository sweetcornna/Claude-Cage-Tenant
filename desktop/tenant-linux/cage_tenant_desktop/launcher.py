"""Entry point for /usr/bin/cage-tenant-desktop."""

from __future__ import annotations

import os
import sys

from . import APP_NAME, PACKAGE
from .policy import webkit_variables_to_clear

USAGE = f"""Usage: {PACKAGE} [--version | --help]

{APP_NAME}: shows a Cage multi-tenant portal (deployed by an administrator on
a Linux Incus server) in its own window. The portal address is entered and
changed in the window; it is the only value this program saves.
"""


def read_version() -> str:
    try:
        with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "VERSION"), encoding="ascii") as handle:
            return handle.read().strip() or "0.0.0"
    except OSError:
        return "0.0.0"


def main(argv=None) -> int:
    args = sys.argv[1:] if argv is None else argv
    if args in (["--version"], ["-V"]):
        print(f"{PACKAGE} {read_version()}")
        return 0
    if args in (["--help"], ["-h"]):
        print(USAGE, end="")
        return 0
    if args:
        sys.stderr.write(USAGE)
        return 2
    # A remote inspector could run script in the portal; WebKit reads these
    # variables when it starts, so remove them first.
    for name in webkit_variables_to_clear(os.environ):
        del os.environ[name]
    try:
        from . import app
    except (ImportError, ValueError) as e:
        sys.stderr.write(
            f"{APP_NAME}: cannot load GTK 3 / WebKitGTK 4.1 ({e}).\n"
            "Install: python3-gi gir1.2-gtk-3.0 gir1.2-webkit2-4.1\n"
        )
        return 1
    return app.run(read_version())


if __name__ == "__main__":
    sys.exit(main())
