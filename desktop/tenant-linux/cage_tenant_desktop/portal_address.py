"""Portal address validation, shared rules with desktop/tenant/PortalAddress.swift.

A portal origin is ``https://<dns-host>[:port]``. The portal serves each VM
from ``vm-<id>.<portal host>``, so the origin must be a DNS name (not an IP
literal) and the root of the site. Only this value is ever stored; accounts,
passwords and cookies stay in the WebKit session.
"""

from __future__ import annotations

import enum
import string
from dataclasses import dataclass
from typing import Optional
from urllib.parse import urlsplit


class PortalAddressError(enum.Enum):
    EMPTY = "empty"
    INVALID_CHARACTERS = "invalidCharacters"
    NOT_URL = "notURL"
    NOT_HTTPS = "notHTTPS"
    USER_INFO = "userInfo"
    QUERY = "query"
    FRAGMENT = "fragment"
    PATH = "path"
    HOST = "host"
    IP_ADDRESS = "ipAddress"
    PORT = "port"

    @property
    def message(self) -> str:
        return _MESSAGES[self]


_MESSAGES = {
    PortalAddressError.EMPTY: "请输入门户地址。",
    PortalAddressError.INVALID_CHARACTERS: "地址只能包含英文可见字符，不能有空格或控制字符。国际化域名请填写 xn-- 开头的形式。",
    PortalAddressError.NOT_URL: "无法识别该地址，格式应为 https://cage.example.org。",
    PortalAddressError.NOT_HTTPS: "门户地址必须以 https:// 开头。",
    PortalAddressError.USER_INFO: "门户地址不能包含用户名或密码。请在门户页面中登录。",
    PortalAddressError.QUERY: "门户地址不能包含查询参数（? 及之后的部分）。",
    PortalAddressError.FRAGMENT: "门户地址不能包含片段（# 及之后的部分）。",
    PortalAddressError.PATH: "请填写门户根地址，不要包含域名之后的路径。",
    PortalAddressError.HOST: "域名无效。请填写门户的完整域名，例如 cage.example.org。",
    PortalAddressError.IP_ADDRESS: "请填写门户域名。虚拟机使用 vm-<id>.<门户域名> 子域名，不支持 IP 地址。",
    PortalAddressError.PORT: "端口无效，应为 1 到 65535 之间的数字。",
}


class InvalidPortalAddress(ValueError):
    def __init__(self, error: PortalAddressError):
        super().__init__(error.message)
        self.error = error


@dataclass(frozen=True)
class PortalOrigin:
    host: str
    port: Optional[int] = None  # None for the default HTTPS port

    @property
    def origin(self) -> str:
        return "https://" + self.host + ("" if self.port is None else f":{self.port}")

    @property
    def url(self) -> str:
        return self.origin + "/"

    @property
    def effective_port(self) -> int:
        return 443 if self.port is None else self.port


# Foundation's whitespacesAndNewlines, so every client trims pasted input the
# same way: Unicode Zs, tab, line breaks and U+200B.
_TRIMMED = "".join(
    [chr(c) for c in range(0x09, 0x0E)]
    + [" ", "\u0085", "\u00a0", "\u1680"]
    + [chr(c) for c in range(0x2000, 0x200C)]
    + ["\u2028", "\u2029", "\u202f", "\u205f", "\u3000"]
)
_LDH = frozenset(string.ascii_lowercase + string.digits + "-")
_DIGITS = frozenset(string.digits)


def _fail(error: PortalAddressError) -> InvalidPortalAddress:
    return InvalidPortalAddress(error)


def validate(text: Optional[str]) -> PortalOrigin:
    """Parse user input into a portal origin; raise InvalidPortalAddress otherwise.

    Specific errors come from a direct reading of the text; urllib must then
    agree on every part, so the stored origin cannot differ from what a URL
    parser sees.
    """
    text = (text or "").strip(_TRIMMED)
    if not text:
        raise _fail(PortalAddressError.EMPTY)
    if len(text) > 2048 or any(not 0x21 <= ord(c) <= 0x7E for c in text):
        raise _fail(PortalAddressError.INVALID_CHARACTERS)
    prefix = "https://"
    lowered = text.lower()
    if not lowered.startswith(prefix):
        raise _fail(PortalAddressError.NOT_URL if lowered.startswith("https:") else PortalAddressError.NOT_HTTPS)

    rest = text[len(prefix):]
    end = next((i for i, c in enumerate(rest) if c in "/?#"), len(rest))
    authority, remainder = rest[:end], rest[end:]
    if "@" in authority:
        raise _fail(PortalAddressError.USER_INFO)
    if "#" in remainder:
        raise _fail(PortalAddressError.FRAGMENT)
    if "?" in remainder:
        raise _fail(PortalAddressError.QUERY)
    if not authority:
        raise _fail(PortalAddressError.HOST)
    if remainder not in ("", "/"):
        raise _fail(PortalAddressError.PATH)
    if authority.startswith("["):
        raise _fail(PortalAddressError.IP_ADDRESS)

    raw_port = None
    host_text, colon, digits = authority.partition(":")
    if colon:
        if not 1 <= len(digits) <= 5 or not set(digits) <= _DIGITS or not 1 <= int(digits) <= 65535:
            raise _fail(PortalAddressError.PORT)
        raw_port = int(digits)
    host = host_text.lower()
    _validate_host(host)

    try:
        parsed = urlsplit(text)
        parsed_port = parsed.port
    except ValueError:
        raise _fail(PortalAddressError.NOT_URL) from None
    if (
        parsed.scheme != "https"
        or parsed.username is not None
        or parsed.password is not None
        or parsed.hostname != host
        or parsed_port != raw_port
        or parsed.path not in ("", "/")
        or parsed.query
        or parsed.fragment
    ):
        raise _fail(PortalAddressError.NOT_URL)
    return PortalOrigin(host, None if raw_port == 443 else raw_port)


def _validate_host(host: str) -> None:
    """LDH names only. A trailing dot is rejected because the gateway matches
    the Host header exactly; a numeric or 0x final label is IPv4 to browsers."""
    if not host or len(host) > 253:
        raise _fail(PortalAddressError.HOST)
    labels = host.split(".")
    for label in labels:
        if not 1 <= len(label) <= 63 or not set(label) <= _LDH or label[0] == "-" or label[-1] == "-":
            raise _fail(PortalAddressError.HOST)
    last = labels[-1]
    if set(last) <= _DIGITS or last.startswith("0x"):
        raise _fail(PortalAddressError.IP_ADDRESS)
    if not "a" <= last[0] <= "z":
        raise _fail(PortalAddressError.HOST)


def try_validate(text: Optional[str]):
    """Return (origin, None) or (None, error)."""
    try:
        return validate(text), None
    except InvalidPortalAddress as e:
        return None, e.error
