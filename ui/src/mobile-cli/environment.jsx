import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Check, Gauge, LoaderCircle, Power, RefreshCw, RotateCcw, Search, Shuffle, Square } from "lucide-react";
import { Doodle } from "./doodles";
import { Confirm, Sheet } from "./sheet";
import { environment } from "./services.mjs";
import { accessDenied } from "./transport.mjs";
import { environmentSummary } from "./environment-state.mjs";
import { haptic } from "./device-navigation.mjs";

export function useEnvironment({ active, visible, onDenied }) {
  const [status, setStatus] = useState(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((n) => n + 1), []);
  useEffect(() => {
    if (!active || !visible) return;
    const controller = new AbortController();
    let timer;
    const load = async () => {
      let delay = 8000;
      try {
        const next = await environment.status({ signal: controller.signal });
        if (controller.signal.aborted) return;
        setStatus(next); setError("");
        if (next?.job?.running || (next && !next.ready && next.vm_running)) delay = 1500;
      } catch (err) {
        if (controller.signal.aborted) return;
        if (accessDenied(err)) { onDenied?.(err); return; }
        setError(err.status === 404 ? "电脑上的 Super Cage 版本较旧，暂不支持在手机上管理环境。请先升级电脑端。" : err.message);
        delay = 5000;
      }
      timer = setTimeout(load, delay);
    };
    void load();
    return () => { clearTimeout(timer); controller.abort(); };
  }, [active, visible, tick, onDenied]);
  return { status, error, refresh, setStatus };
}

const confirmText = {
  stop: ["停止环境？", "当前终端和正在运行的对话会结束。项目文件和浏览器登录会保留。", "停止环境"],
  restart: ["重启环境？", "环境会停止后重新启动，当前终端会话会结束；项目文件和浏览器登录会保留，重新检测通过前外网保持关闭。", "重启环境"],
};

export function EnvironmentView({ env, onDenied }) {
  const { status, error, refresh, setStatus } = env;
  const [ask, setAsk] = useState("");
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const [nodes, setNodes] = useState(false);
  const summary = environmentSummary(status);
  const run = async (action) => {
    setBusy(true); setActionError("");
    try {
      const job = await environment.run(action);
      haptic("success");
      setStatus((old) => old && ({ ...old, job: { ...job, running: true } }));
      setAsk(""); refresh();
    } catch (err) {
      if (accessDenied(err)) onDenied?.(err); else setActionError(err.message);
      haptic("warning");
    } finally { setBusy(false); }
  };
  const ready = !!status?.ready;
  const running = !!status?.job?.running;
  const stopped = status && !status.ready && !status.vm_running && !running;
  const failed = status?.job && !status.job.running && status.job.error;
  return <section className="environment page-scroll" aria-label="环境">
    <div className={"env-hero " + summary.tone}>
      <Doodle name={summary.doodle} className={"env-doodle" + (summary.busy ? " sway" : "")} />
      <h1>{summary.title}</h1>
      {summary.detail && <p>{summary.detail}</p>}
      {status?.guest_system && <small className="env-system">{status.guest_system}</small>}
    </div>
    {error && <div className="notice" role="alert">{error}</div>}
    {(actionError || failed) && <div className="notice" role="alert">{actionError || status.job.error}{status?.job?.recovery && <p className="note">{status.job.recovery}</p>}</div>}
    <div className="env-actions">
      {stopped ? <button type="button" className="action clay wide" disabled={busy} onClick={() => run("start")}>
        {busy ? <LoaderCircle size={18} className="spin" /> : <Power size={18} />}启动环境</button>
        : <>
          <button type="button" className="action secondary" disabled={busy || running || !status} onClick={() => setAsk("restart")}><RotateCcw size={17} />重启</button>
          <button type="button" className="action secondary danger-text" disabled={busy || running || !status || (!ready && !status?.vm_running)} onClick={() => setAsk("stop")}><Square size={15} />停止</button>
        </>}
    </div>
    <section className="card env-network" aria-label="网络">
      <h2>网络</h2>
      <dl>
        <div><dt>外网</dt><dd><span className={"chip " + (status?.network?.mode === "open" ? "ok" : status?.network?.mode === "checking" ? "busy" : "off")}>
          {status?.network?.mode === "open" ? "已开启" : status?.network?.mode === "checking" ? "检测中" : "已关闭"}</span></dd></div>
        {status?.network?.exit_ip && <div><dt>出口地址</dt><dd className="mono">{status.network.exit_ip}</dd></div>}
        <div><dt>代理节点</dt><dd>{status?.proxy?.selected_name || status?.proxy?.state_label || "未设置"}</dd></div>
      </dl>
      {status?.network_error && <p className="note">{status.network_error}</p>}
      <button type="button" className="action secondary wide" disabled={!status || running} onClick={() => setNodes(true)}><Shuffle size={17} />切换节点</button>
    </section>
    <p className="env-foot note">停止或重启会结束终端；项目文件和浏览器登录会保留。<button type="button" className="link" onClick={refresh}><RefreshCw size={13} />刷新</button></p>
    {ask && <Confirm title={confirmText[ask][0]} confirm={confirmText[ask][2]} danger={ask === "stop"} busy={busy}
      onCancel={() => setAsk("")} onConfirm={() => run(ask)}>{confirmText[ask][1]}</Confirm>}
    {nodes && <NodePicker selected={status?.proxy?.selected} onClose={() => setNodes(false)} onDenied={onDenied}
      onSwitched={() => { setNodes(false); refresh(); }} />}
  </section>;
}

function NodePicker({ selected, onClose, onSwitched, onDenied }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [delays, setDelays] = useState({});
  const [testing, setTesting] = useState(false);
  const [choice, setChoice] = useState(null);
  const [busy, setBusy] = useState(false);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);
  useEffect(() => {
    const controller = new AbortController();
    environment.proxy({ signal: controller.signal }).then(setData).catch((err) => {
      if (controller.signal.aborted) return;
      if (accessDenied(err)) onDenied?.(err); else setError(err.message);
    });
    return () => controller.abort();
  }, [onDenied]);
  const list = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data?.sources || []).map((source) => ({ ...source, nodes: (source.nodes || []).filter((n) => !q || n.name.toLowerCase().includes(q)) }))
      .filter((source) => source.nodes.length);
  }, [data, query]);
  const test = async () => {
    const pending = list.flatMap((s) => s.nodes).slice(0, 40);
    setTesting(true);
    let next = 0;
    const worker = async () => {
      while (alive.current && next < pending.length) {
        const node = pending[next++];
        setDelays((old) => ({ ...old, [node.id]: "…" }));
        try { const r = await environment.delay(node.id); if (alive.current) setDelays((old) => ({ ...old, [node.id]: r?.delay > 0 ? r.delay : "超时" })); }
        catch { if (alive.current) setDelays((old) => ({ ...old, [node.id]: "失败" })); }
      }
    };
    await Promise.all([worker(), worker(), worker(), worker()]);
    if (alive.current) setTesting(false);
  };
  const confirm = async () => {
    setBusy(true); setError("");
    try { await environment.select(choice.id); haptic("success"); onSwitched(); }
    catch (err) { if (accessDenied(err)) onDenied?.(err); else setError(err.message); setChoice(null); }
    finally { if (alive.current) setBusy(false); }
  };
  return <Sheet title="切换节点" onClose={onClose} full className="node-picker">
    <div className="search-field"><Search size={16} /><input type="search" placeholder="搜索节点" value={query} onChange={(e) => setQuery(e.target.value)} />
      <button type="button" className="chip-button" disabled={testing || !list.length} onClick={test}>{testing ? <LoaderCircle size={14} className="spin" /> : <Gauge size={14} />}测速</button></div>
    {error && <div className="notice" role="alert">{error}</div>}
    {!data && !error && <p className="note center"><LoaderCircle size={16} className="spin" /> 正在读取节点…</p>}
    {data && !list.length && <p className="note center">{query ? "没有匹配的节点。" : "还没有可用节点，请在电脑上导入订阅。"}</p>}
    {list.map((source) => <section key={source.id} className="node-source">
      <h3>{source.name}{source.error && <small className="bad"> · {source.error}</small>}</h3>
      <ul>{source.nodes.map((node) => <li key={node.id}><button type="button" aria-current={node.id === selected ? "true" : undefined} onClick={() => node.id !== selected && setChoice(node)}>
        <span className="node-name">{node.name}<small>{node.type}</small></span>
        {delays[node.id] != null && <span className={"delay" + (typeof delays[node.id] === "number" ? delays[node.id] < 300 ? " fast" : delays[node.id] < 800 ? " ok" : " slow" : "")}>{typeof delays[node.id] === "number" ? delays[node.id] + " ms" : delays[node.id]}</span>}
        {node.id === selected && <Check size={17} className="node-check" />}
      </button></li>)}</ul>
    </section>)}
    {choice && <Confirm title={"切换到 " + choice.name + "？"} confirm="切换" busy={busy} onCancel={() => setChoice(null)} onConfirm={confirm}>
      切换期间外网会短暂关闭，重新检测通过后自动恢复。</Confirm>}
  </Sheet>;
}
