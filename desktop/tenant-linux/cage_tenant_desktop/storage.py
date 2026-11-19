"""Everything the client writes: the portal origin and per-origin WebKit sessions.

* ``$XDG_CONFIG_HOME/cage-tenant-desktop/portal-origin`` holds the normalized
  portal origin. It is the only value the client itself persists.
* ``$XDG_DATA_HOME/cage-tenant-desktop/sessions/<key>`` and
  ``$XDG_CACHE_HOME/cage-tenant-desktop/sessions/<key>`` are the WebKit
  website-data and cache folders for one portal origin. WebKit keeps cookies
  and site storage there; the client never reads them, only deletes them.
"""

from __future__ import annotations

import hashlib
import os
import shutil
import stat
from typing import List, Mapping, Optional, Tuple

from . import PACKAGE
from .portal_address import PortalOrigin, try_validate

ORIGIN_FILE = "portal-origin"
SESSIONS = "sessions"
_MAX_ORIGIN_BYTES = 4096


def _xdg(environ: Mapping[str, str], name: str, home: str, default: str) -> str:
    value = environ.get(name, "")
    # The XDG spec says relative values are invalid and must be ignored.
    return value if os.path.isabs(value) else os.path.join(home, default)


def session_key(origin: PortalOrigin) -> str:
    """Stable per-origin folder name; different servers never share a session."""
    return hashlib.sha256(origin.origin.encode("ascii")).hexdigest()[:32]


def _remove(path: str) -> bool:
    """Delete a file, link or folder without following links. False on failure."""
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        return True
    except OSError:
        return False
    try:
        if stat.S_ISDIR(info.st_mode):
            shutil.rmtree(path)
        else:
            os.unlink(path)
        return True
    except FileNotFoundError:
        return True
    except OSError:
        return False


class PortalStore:
    def __init__(self, config_dir: str, data_dir: str, cache_dir: str):
        self.config_dir = os.path.abspath(config_dir)
        self.data_dir = os.path.abspath(data_dir)
        self.cache_dir = os.path.abspath(cache_dir)

    @classmethod
    def for_environment(cls, environ: Mapping[str, str] = os.environ, home: Optional[str] = None) -> "PortalStore":
        home = home or os.path.expanduser("~")
        return cls(
            os.path.join(_xdg(environ, "XDG_CONFIG_HOME", home, ".config"), PACKAGE),
            os.path.join(_xdg(environ, "XDG_DATA_HOME", home, os.path.join(".local", "share")), PACKAGE),
            os.path.join(_xdg(environ, "XDG_CACHE_HOME", home, ".cache"), PACKAGE),
        )

    @property
    def origin_file(self) -> str:
        return os.path.join(self.config_dir, ORIGIN_FILE)

    # ---- portal origin ---------------------------------------------------

    def load(self) -> Optional[PortalOrigin]:
        """The stored origin, validated again. Anything else found there
        (an invalid value, a link, an oversized file) is removed."""
        path = self.origin_file
        try:
            info = os.lstat(path)
        except FileNotFoundError:
            return None
        if not stat.S_ISREG(info.st_mode) or info.st_size > _MAX_ORIGIN_BYTES:
            if not stat.S_ISDIR(info.st_mode):
                self.clear()
            return None
        try:
            fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0))
            with os.fdopen(fd, "rb") as handle:
                raw = handle.read(_MAX_ORIGIN_BYTES + 1)
            text = raw.decode("utf-8")
        except (OSError, UnicodeDecodeError):
            self.clear()
            return None
        origin, _ = try_validate(text)
        if origin is None:
            self.clear()
        return origin

    def save(self, origin: PortalOrigin) -> None:
        """Atomically replace the origin file (mode 0600 in a 0700 folder)."""
        os.makedirs(self.config_dir, mode=0o700, exist_ok=True)
        temp = os.path.join(self.config_dir, f".{ORIGIN_FILE}.{os.getpid()}.{os.urandom(6).hex()}")
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
        try:
            with os.fdopen(fd, "w", encoding="ascii") as handle:
                handle.write(origin.origin + "\n")
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(temp, self.origin_file)
        finally:
            if os.path.lexists(temp):
                os.unlink(temp)

    def clear(self) -> None:
        try:
            os.unlink(self.origin_file)
        except FileNotFoundError:
            pass
        except IsADirectoryError:
            pass

    # ---- WebKit sessions -------------------------------------------------

    def session_dirs(self, origin: PortalOrigin, create: bool = False) -> Tuple[str, str]:
        key = session_key(origin)
        dirs = (os.path.join(self.data_dir, SESSIONS, key), os.path.join(self.cache_dir, SESSIONS, key))
        if create:
            for path in dirs:
                os.makedirs(path, mode=0o700, exist_ok=True)
                os.chmod(path, 0o700)
        return dirs

    def remove_session(self, origin: PortalOrigin) -> bool:
        results = [_remove(path) for path in self.session_dirs(origin)]
        return all(results)

    def remove_sessions_except(self, keep: Optional[PortalOrigin]) -> List[str]:
        """Delete every session except ``keep``'s. Run at startup so data from
        a previous server goes away even if an earlier deletion failed."""
        keep_name = session_key(keep) if keep else None
        failed = []
        for root in (os.path.join(self.data_dir, SESSIONS), os.path.join(self.cache_dir, SESSIONS)):
            try:
                names = os.listdir(root)
            except FileNotFoundError:
                continue
            except NotADirectoryError:
                if not _remove(root):
                    failed.append(root)
                continue
            for name in names:
                path = os.path.join(root, name)
                if name == keep_name and os.path.isdir(path) and not os.path.islink(path):
                    continue
                if not _remove(path):
                    failed.append(path)
        return failed
