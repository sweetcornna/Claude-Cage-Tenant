"""Hand off to the upstream Cage desktop app, or its pinned Debian package.

Only the native start page uses this module. The portal WebView has no
access to it.
"""

from __future__ import annotations

import errno
import hashlib
import http.client
import os
import platform
import subprocess
import urllib.request

INSTALLED = "/usr/bin/cage-desktop"
DPKG = "/usr/bin/dpkg"
NAME = "Cage-0.1.8-linux-amd64.deb"
SIZE = 121_173_898
SHA256 = "7eb68b00dd94c26c3b2b3408c33d1e047eafa08a10bb6ca8c85118896345e018"
URL = "https://github.com/Lee-xeo/Claude-Cage/releases/download/v0.1.8/" + NAME

DOWNLOAD_FAILED = "无法下载安装包，请检查网络后重试。"
CHECKSUM_FAILED = "安装包校验失败，请重试。"
DISK_FAILED = "无法保存安装包，请检查磁盘空间后重试。"


class LocalCageError(Exception):
    pass


def installed() -> bool:
    return os.path.isfile(INSTALLED) and os.access(INSTALLED, os.X_OK)


def launch() -> None:
    try:
        subprocess.Popen(
            [INSTALLED],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
            close_fds=True,
        )
    except OSError as e:
        raise LocalCageError("无法打开本地 Cage。") from e


def supported(machine: str | None = None, dpkg: str = DPKG) -> bool:
    """The upstream package is amd64-only and needs a dpkg-based system."""
    return (machine or platform.machine()).lower() in ("x86_64", "amd64") and os.path.isfile(dpkg)


def verified(path: str) -> bool:
    try:
        if os.path.getsize(path) != SIZE:
            return False
        digest = hashlib.sha256()
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                digest.update(chunk)
        return digest.hexdigest() == SHA256
    except OSError:
        return False


def _unlink(path: str) -> None:
    try:
        os.unlink(path)
    except FileNotFoundError:
        pass


def prepare_installer(folder: str, opener=urllib.request.urlopen) -> str:
    """Return a verified copy of the upstream package in folder, downloading it if needed.

    The partial file has a fixed name, so one left by an interrupted run is
    replaced rather than kept. Network failures, stalls (60 s per read) and
    malformed responses raise DOWNLOAD_FAILED; disk failures raise DISK_FAILED.
    """
    try:
        os.makedirs(folder, mode=0o700, exist_ok=True)
    except OSError as e:
        raise LocalCageError(DISK_FAILED) from e
    target = os.path.join(folder, NAME)
    if verified(target):
        return target
    partial = os.path.join(folder, "." + NAME + ".part")
    try:
        _unlink(partial)
        fd = os.open(partial, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    except OSError as e:
        raise LocalCageError(DISK_FAILED) from e
    try:
        with os.fdopen(fd, "wb") as out:
            try:
                response = opener(URL, timeout=60)
            except (OSError, http.client.HTTPException, ValueError) as e:
                raise LocalCageError(DOWNLOAD_FAILED) from e
            with response:
                if getattr(response, "status", 200) != 200:
                    raise LocalCageError(DOWNLOAD_FAILED)
                received = 0
                while True:
                    try:
                        chunk = response.read(1 << 20)
                    except (OSError, http.client.HTTPException, ValueError) as e:
                        raise LocalCageError(DOWNLOAD_FAILED) from e
                    if not chunk:
                        break
                    received += len(chunk)
                    if received > SIZE:
                        raise LocalCageError(CHECKSUM_FAILED)
                    try:
                        out.write(chunk)
                    except OSError as e:
                        raise LocalCageError(DISK_FAILED) from e
        if not verified(partial):
            raise LocalCageError(CHECKSUM_FAILED)
        try:
            os.replace(partial, target)
        except OSError as e:
            raise LocalCageError(DISK_FAILED) from e
        return target
    except OSError as e:
        # Closing the file can still report a full disk.
        if e.errno in (errno.ENOSPC, errno.EDQUOT, errno.EIO):
            raise LocalCageError(DISK_FAILED) from e
        raise LocalCageError(DOWNLOAD_FAILED) from e
    finally:
        try:
            _unlink(partial)
        except OSError:
            pass
