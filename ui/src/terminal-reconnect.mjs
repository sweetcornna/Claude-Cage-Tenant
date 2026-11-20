// Terminal streams pass through proxies, relays and tunnels that drop a
// WebSocket after a period of silence. While the page is visible the view
// sends a small keepalive, and a dropped stream reconnects on its own instead
// of waiting for the user to press "reconnect".

export const terminalKeepaliveMs = 25_000;
export const terminalKeepaliveMessage = JSON.stringify({ type: "ping" });

// A stream that stayed open this long was dropped by the network, not refused.
const stableMs = 10_000;
// After this many quick failures in a row the view stops and offers the button.
export const terminalReconnectLimit = 6;

// failures counts consecutive attempts that closed soon after connecting
// (or never connected). The returned delay is 0 when the view should stop.
export function terminalReconnectPlan(failures, openedFor) {
  const next = openedFor >= stableMs ? 1 : failures + 1;
  if (next > terminalReconnectLimit) return { failures: next, delay: 0 };
  return { failures: next, delay: Math.min(500 * 2 ** (next - 1), 15_000) };
}
