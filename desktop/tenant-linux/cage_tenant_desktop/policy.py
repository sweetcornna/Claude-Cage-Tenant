"""Pure decisions used by the window, kept apart from GTK so they run in tests."""

from __future__ import annotations

import enum
import re
from dataclasses import dataclass
from typing import Iterable, List, Mapping, Optional

from .portal_address import PortalAddressError, PortalOrigin, try_validate

# WebKit normalizes URIs before signals see them; anything that does not
# match this strict shape is treated as another origin.
_HTTPS_AUTHORITY = re.compile(r"^https://([a-z0-9.-]+)(?::([0-9]{1,5}))?(?:[/?#]|$)", re.IGNORECASE)
_VM_LABEL = re.compile(r"^vm-[0-9a-f]{32}$")
# Schemes a subframe may legitimately use. Navigation decisions in WebKitGTK
# do not say which frame they are for, so only clearly non-web schemes are
# refused there; the main-frame origin rule is enforced on the response.
_WEB_SCHEMES = ("https:", "http:", "about:", "data:", "blob:")


def _authority(uri: Optional[str]):
    if not uri or len(uri) > 65536:
        return None
    match = _HTTPS_AUTHORITY.match(uri)
    if not match:
        return None
    port = int(match.group(2)) if match.group(2) else 443
    return match.group(1).lower(), port


def is_portal_document(uri: Optional[str], origin: PortalOrigin) -> bool:
    """Main-frame rule: only documents on the exact portal origin (scheme,
    host and port). Subframes are not checked; the portal's CSP, iframe
    sandbox and server authentication govern them."""
    parts = _authority(uri)
    return parts == (origin.host, origin.effective_port)


def is_portal_family(uri: Optional[str], origin: PortalOrigin) -> bool:
    """The portal origin or one of its VM origins (vm-<32 hex>.<host>)."""
    parts = _authority(uri)
    if not parts or parts[1] != origin.effective_port:
        return False
    host = parts[0]
    if host == origin.host:
        return True
    suffix = "." + origin.host
    return host.endswith(suffix) and bool(_VM_LABEL.match(host[: -len(suffix)]))


def is_web_navigation(uri: Optional[str]) -> bool:
    return bool(uri) and uri.lower().startswith(_WEB_SCHEMES)


def describe(uri: Optional[str]) -> str:
    """Scheme and host only, for notices; never the path, query or fragment."""
    if not uri:
        return "未知地址"
    match = re.match(r"^([a-z][a-z0-9+.-]*):(//([^/?#@]*@)?([^/?#]*))?", uri, re.IGNORECASE)
    if not match:
        return "未知地址"
    text = match.group(1).lower() + ":" + ("//" + match.group(4) if match.group(2) else "")
    return text if len(text) <= 100 else text[:100] + "…"


class ConnectAction(enum.Enum):
    REJECT = "reject"    # invalid input: show the error, change nothing
    CONNECT = "connect"  # first server: save it, open a fresh session
    KEEP = "keep"        # same server: keep its session
    SWITCH = "switch"    # other server: destroy the old view and session first


@dataclass(frozen=True)
class ConnectPlan:
    action: ConnectAction
    origin: Optional[PortalOrigin] = None
    error: Optional[PortalAddressError] = None

    @property
    def message(self) -> Optional[str]:
        return self.error.message if self.error else None


def plan_connect(text: Optional[str], current: Optional[PortalOrigin]) -> ConnectPlan:
    origin, error = try_validate(text)
    if origin is None:
        return ConnectPlan(ConnectAction.REJECT, None, error)
    if current is None:
        return ConnectPlan(ConnectAction.CONNECT, origin)
    return ConnectPlan(ConnectAction.KEEP if origin == current else ConnectAction.SWITCH, origin)


def webkit_variables_to_clear(environ: Mapping[str, str]) -> List[str]:
    """WEBKIT_INSPECTOR_SERVER and friends open a remote inspector that can
    run script in the page. The launcher removes them before loading WebKit."""
    return sorted(name for name in environ if name.upper().startswith("WEBKIT_INSPECTOR"))


def removable_context_actions(names: Iterable[str]) -> List[str]:
    """Context-menu actions that would open a window or the inspector."""
    return [n for n in names if "NEW_WINDOW" in n or n == "INSPECT_ELEMENT"]
