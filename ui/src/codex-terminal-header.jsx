import React, { useState } from "react";
import { LoaderCircle, Pencil, Pin, PinOff, RotateCcw, Square } from "lucide-react";
import { api } from "./api";
import { useSessionSnapshot } from "./cli-sessions";
import { codexStateWord, sessionOwner } from "./session-provider.mjs";
import "./codex-terminal-header.css";

// Pieces of the header of a bound Codex terminal (a tab whose
// cli_session_provider is "codex"). They read the session's row from the list
// store themselves, so the workbench does not re-render for them.
const useRow = (store, id) => useSessionSnapshot(store, (list) => list.sessions.find((session) => session.id === id) || null);

export function CodexTerminalDetail({ store, tab, label }) {
  const row = useRow(store, tab.cli_session_id);
  const state = codexStateWord(row || { provider: "codex", owner: "desktop" }, { closed: tab.closed });
  return <>{["Codex", label, state].filter(Boolean).join(" · ")}</>;
}

// Stop (Esc into the TUI, through the server), pin, rename; reopen once Codex
// has quit. A refusal shows here, in the header, in the server's words.
export function CodexTerminalActions({ store, tab, pinned, busy, onPin, onRename, onReopen }) {
  const row = useRow(store, tab.cli_session_id);
  const [stop, setStop] = useState({ busy: false, error: "" });
  const running = !tab.closed && Boolean(row?.active) && sessionOwner(row) === "desktop";
  const stopTurn = async () => {
    setStop({ busy: true, error: "" });
    try {
      const path = "/cli/sessions/" + encodeURIComponent(tab.cli_session_id);
      // The list rows carry no turns: read the view for the running turn's id.
      const view = await api(path);
      const last = view.turns?.at(-1);
      if (!last || last.status !== "running") throw new Error("本轮运行状态已变化，请重新读取");
      await api(path + "/stop", { request_id: last.id });
      setStop({ busy: false, error: "" });
    } catch (error) {
      setStop({ busy: false, error: error.message });
    }
  };
  return (
    <span className="codex-terminal-actions">
      {stop.error && <span className="codex-terminal-error" role="alert" title={stop.error}>{stop.error}</span>}
      {running && (
        <button className="icon-button" aria-label="停止本轮" title="停止本轮（向 Codex 发送 Esc）" disabled={stop.busy || busy} onClick={stopTurn}>
          {stop.busy ? <LoaderCircle size={15} className="spin" /> : <Square size={14} />}
        </button>
      )}
      {tab.closed && onReopen && (
        <button className="icon-button" aria-label="重新打开会话" title="重新打开会话" disabled={busy} onClick={onReopen}>
          <RotateCcw size={15} />
        </button>
      )}
      <button className="icon-button" aria-label="重命名会话" title="重命名会话" disabled={busy} onClick={onRename}><Pencil size={15} /></button>
      <button className="icon-button" aria-label={pinned ? "取消置顶" : "置顶会话"} title={pinned ? "取消置顶" : "置顶会话"} disabled={busy} onClick={onPin}>
        {pinned ? <PinOff size={15} /> : <Pin size={15} />}
      </button>
    </span>
  );
}
