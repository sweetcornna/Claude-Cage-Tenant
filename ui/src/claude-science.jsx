import React, { useEffect, useRef, useState } from "react";
import { Download, ExternalLink, Play, Power, RefreshCw, RotateCcw } from "lucide-react";
import { api } from "./api";
import { Drawing } from "./drawings";
import { SafetyMessage } from "./safety-message";
import { useWindowVisibility } from "./window-visibility";
import { scienceView } from "./claude-science-state.mjs";
import "./claude-science.css";

// Claude Science (internal/workbench/claude_science.go): install, start, stop
// and update are Cage jobs followed through the workbench status; open mints a
// single-use link on the server and returns only the browser tab to show. The
// link and the response bodies never reach this file's logs or messages.
const confirmations = {
  stop: { title: "停止 Claude Science？", body: "正在进行的分析会中断，已打开的 Claude Science 页面将失效。项目和登录状态保留，之后可以再次启动。", confirm: "停止" },
  update: { title: "更新 Claude Science？", body: "更新会先停止 Claude Science，再安装新版本，已打开的页面将失效。项目和登录状态保留，更新后需要重新启动。", confirm: "更新" },
};
const icons = { install: Download, start: Play, open: ExternalLink, stop: Power, update: RefreshCw, refresh: RotateCcw };

export function ClaudeSciencePanel({ status, onOpen, onConfirm, onDetails }) {
  const windowVisible = useWindowVisibility();
  const running = Boolean(status.vm_running);
  const [data, setData] = useState(null);
  const [readError, setReadError] = useState(null);
  const [actionError, setActionError] = useState("");
  const [pending, setPending] = useState("");
  const [jobID, setJobID] = useState("");
  const mounted = useRef(true);
  const request = useRef(null);
  const revision = useRef(0);
  const submitted = useRef(null);
  const finishing = useRef("");
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  const refresh = async (valid = () => true) => {
    const current = revision.current;
    const read = request.current ??= api("/claude-science");
    try {
      const value = await read;
      if (!valid() || current !== revision.current) return null;
      setData(value);
      setReadError(null);
      return value;
    } catch (e) {
      if (valid() && current === revision.current) setReadError({ status: e.status, message: e.message });
      return null;
    } finally {
      if (request.current === read) request.current = null;
    }
  };
  useEffect(() => {
    if (!windowVisible || !running || status.job?.running) return;
    let disposed = false;
    let timer;
    const poll = async () => {
      await refresh(() => !disposed);
      if (disposed) return;
      // A job this pane started that the status feed never reported back.
      if (submitted.current && Date.now() - submitted.current > 30000) {
        submitted.current = null;
        setJobID("");
        setPending("");
      }
      timer = setTimeout(poll, 5000);
    };
    poll();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, [windowVisible, running, status.job?.running]);
  useEffect(() => {
    if (!jobID || status.job?.id !== jobID || status.job.running || finishing.current === jobID) return;
    // Keep the job's state on screen until the re-read answers, so the pane
    // does not flash the state from before the job.
    finishing.current = jobID;
    void refresh().finally(() => {
      submitted.current = null;
      setJobID("");
      setPending("");
    });
  }, [status.job, jobID]);
  const submit = async (name) => {
    revision.current++;
    request.current = null;
    setPending(name);
    setActionError("");
    try {
      const job = await api(`/claude-science/${name}`, {});
      submitted.current = Date.now();
      setJobID(job.id);
    } catch (e) {
      setActionError(e.message);
      setPending("");
    }
  };
  const open = async () => {
    setPending("open");
    setActionError("");
    try {
      const tab = await api("/claude-science/open", {});
      onOpen({ id: tab.id, kind: tab.kind, title: tab.title });
    } catch (e) {
      if (mounted.current) setActionError(e.message);
    } finally {
      if (mounted.current) setPending("");
    }
  };
  const act = async (item) => {
    if (item.action === "refresh") { setReadError(null); void refresh(); return; }
    if (item.action === "open") { await open(); return; }
    const ask = item.confirm && confirmations[item.action];
    if (ask && !await onConfirm(ask)) return;
    await submit(item.action);
  };
  // The details dialog reads status.job; offer it only when a job is on record.
  const details = status.job?.log ? onDetails : undefined;
  const view = scienceView({ data, error: readError, actionError, job: status.job, running, ready: Boolean(status.ready), pending });
  const button = (item, className) => {
    const Icon = icons[item.action];
    return (
      <button key={item.action} className={className} disabled={item.disabled} onClick={() => void act(item)}>
        {Icon && <Icon size={16} aria-hidden="true" />}
        {item.label}
      </button>
    );
  };
  return (
    <section className="claude-science" data-state={view.state} aria-busy={view.state === "loading" || undefined}>
      {view.notices.map((notice, index) => (
        <div key={index} className="science-notice" data-tone={notice.tone} role={notice.tone === "error" ? "alert" : "status"}>
          <span>
            {notice.label && <strong>{notice.label}</strong>}
            <SafetyMessage message={notice.text} />
          </span>
          {notice.details && details && <button className="quiet-button" onClick={details}>操作详情</button>}
        </div>
      ))}
      <div className={"science-card" + (view.drawing ? " hero" : "")}>
        {view.drawing && <Drawing name={view.drawing} />}
        <div className="science-head">
          {view.tone && <span className={"status-dot " + view.tone} aria-hidden="true" />}
          <h2 className="science-title">{view.title}</h2>
          {view.chips.length > 0 && (
            <div className="science-chips">
              {view.chips.map((chip) => <span key={chip.text} className={"chip" + (chip.tone ? " " + chip.tone : "")}>{chip.text}</span>)}
            </div>
          )}
        </div>
        {view.lead && <p className="science-lead">{view.lead}</p>}
        {view.facts.length > 0 && (
          <dl className="science-facts">
            {view.facts.map((fact) => (
              <div key={fact.label} data-tone={fact.tone || undefined}>
                <dt>{fact.label}</dt>
                <dd>{fact.value}</dd>
              </div>
            ))}
          </dl>
        )}
        {view.note && <p className="science-note">{view.note}</p>}
        {view.progress && (
          <div className="science-progress" role="status">
            <span className="live-dot" aria-hidden="true" />
            <span className="science-progress-text">{view.progress.message}</span>
            {details && <button className="text-button" onClick={details}>查看进度</button>}
          </div>
        )}
        {view.log.length > 0 && <pre className="science-log" aria-label="操作日志">{view.log.join("\n")}</pre>}
        {(view.primary || view.secondary.length > 0) && (
          <div className="science-actions">
            {view.primary && button(view.primary, "clay large")}
            {view.secondary.map((item) => button(item, "quiet-button"))}
          </div>
        )}
        {view.hint && <p className="science-hint">{view.hint}</p>}
      </div>
    </section>
  );
}
