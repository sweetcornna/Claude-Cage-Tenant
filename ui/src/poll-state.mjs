// The workbench polls its status every few seconds. Each poll returns fresh
// objects, so keeping the previous reference when nothing changed lets React
// skip re-rendering the whole workbench and its terminals.

// sameValue compares JSON-like values by content.
export function sameValue(a, b) {
  if (a === b) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null || Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a)) return a.length === b.length && a.every((item, index) => sameValue(item, b[index]));
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) => Object.hasOwn(b, key) && sameValue(a[key], b[key]));
}

// keepSame returns previous when next has the same content.
export function keepSame(previous, next) {
  return sameValue(previous, next) ? previous : next;
}

// reuseItems keeps each unchanged item of previous (matched by id) and the
// previous list itself when no item, and no position, changed.
export function reuseItems(previous, next) {
  const byId = new Map(previous.map((item) => [item.id, item]));
  let changed = previous.length !== next.length;
  const merged = next.map((item, index) => {
    const old = byId.get(item.id);
    const kept = old !== undefined && sameValue(old, item) ? old : item;
    if (kept !== previous[index]) changed = true;
    return kept;
  });
  return changed ? merged : previous;
}

// After this many polls the terminal list is read even when the status
// reports no change, as a safety net.
export const terminalListRefreshPolls = 12;

// terminalListDue reports whether a status poll must also read the terminal
// list: on request, before the first list, when the server's revision changed
// (or a server without one), or after terminalListRefreshPolls polls.
export function terminalListDue({ force = false, cached, revision, lastRevision, polls }) {
  return force || !cached || typeof revision !== "string" || revision !== lastRevision || polls >= terminalListRefreshPolls;
}
