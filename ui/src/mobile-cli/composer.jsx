import React, { useCallback, useEffect, useRef, useState } from "react";
import { ArrowUp, FileText, ImageIcon, LoaderCircle, Paperclip, Plus, Square, X } from "lucide-react";
import { attachmentDir, files, formatSize } from "./services.mjs";
import { haptic } from "./device-navigation.mjs";

const stamp = () => {
  const d = new Date(), p = (n) => String(n).padStart(2, "0");
  return d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + "-" + p(d.getHours()) + p(d.getMinutes()) + p(d.getSeconds());
};
const safeName = (name) => (name || "file").normalize("NFC").replace(/[\u0000-\u001f\u007f/\\:*?"<>|]/g, "_").replace(/^\.+/, "").slice(-120) || "file";

// Attachments are uploaded into the project so Claude Code or Codex can read
// them by path; the message then lists the project-relative paths.
export function useAttachments(project) {
  const [items, setItems] = useState([]);
  const controllers = useRef(new Map());
  useEffect(() => () => { for (const c of controllers.current.values()) c.abort(); }, []);
  const add = useCallback((list) => {
    if (!project) return;
    for (const file of list) {
      const id = crypto.randomUUID();
      const name = stamp() + "-" + safeName(file.name);
      const controller = new AbortController();
      controllers.current.set(id, controller);
      setItems((old) => [...old, { id, name: file.name || name, size: file.size, image: (file.type || "").startsWith("image/"), status: "uploading", progress: 0 }]);
      const update = (fields) => setItems((old) => old.map((item) => item.id === id ? { ...item, ...fields } : item));
      files.upload(project, attachmentDir, file, { name, signal: controller.signal, onProgress: (done, total) => update({ progress: total ? done / total : 1 }) })
        .then((result) => { update({ status: "done", path: result.path || attachmentDir + "/" + name, progress: 1 }); haptic("light"); })
        .catch((error) => { if (!controller.signal.aborted) update({ status: "error", error: error.message }); })
        .finally(() => controllers.current.delete(id));
    }
  }, [project]);
  const remove = useCallback((id) => {
    controllers.current.get(id)?.abort();
    setItems((old) => old.filter((item) => item.id !== id));
  }, []);
  const clear = useCallback(() => setItems([]), []);
  const uploading = items.some((item) => item.status === "uploading");
  const suffix = () => {
    const done = items.filter((item) => item.status === "done");
    return done.length ? "\n\n附件（项目内路径）：\n" + done.map((item) => "- `" + item.path + "`").join("\n") : "";
  };
  return { items, add, remove, clear, uploading, suffix, enabled: !!project };
}

export function AttachmentStrip({ attachments }) {
  if (!attachments.items.length) return null;
  return <ul className="attachment-strip" aria-label="附件">
    {attachments.items.map((item) => <li key={item.id} className={"attachment " + item.status}>
      {item.status === "uploading" ? <LoaderCircle size={15} className="spin" /> : item.image ? <ImageIcon size={15} /> : <FileText size={15} />}
      <span className="attachment-name">{item.name}<small>{item.status === "error" ? item.error : item.status === "uploading" ? Math.round(item.progress * 100) + "%" : formatSize(item.size)}</small></span>
      <button type="button" aria-label={"移除 " + item.name} onClick={() => attachments.remove(item.id)}><X size={14} /></button>
    </li>)}
  </ul>;
}

export function Composer({ value, onChange, onSubmit, placeholder, disabled, canSend, running, stopping, onStop, busy, attachments, chips, inputRef, onKeySubmit = true }) {
  const fileInput = useRef(null);
  const own = useRef(null);
  const input = inputRef || own;
  useEffect(() => {
    const el = input.current;
    if (!el) return;
    el.style.height = "24px";
    el.style.height = Math.min(el.scrollHeight, 168) + "px";
  }, [value, input]);
  return <form className="composer" onSubmit={(event) => { event.preventDefault(); if (canSend) { haptic("light"); onSubmit(); } }}>
    {attachments && <AttachmentStrip attachments={attachments} />}
    <textarea ref={input} aria-label="消息" rows="1" placeholder={placeholder} value={value} disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
      onKeyDown={(event) => {
        if (onKeySubmit && event.key === "Enter" && !event.nativeEvent.isComposing && (event.ctrlKey || event.metaKey)) {
          event.preventDefault(); if (canSend) onSubmit();
        }
      }} />
    <div className="composer-tools">
      {attachments && <>
        <button type="button" className="composer-icon" aria-label="添加照片或文件" title={attachments.enabled ? "添加照片或文件" : "先选择项目"}
          disabled={!attachments.enabled || disabled} onClick={() => fileInput.current?.click()}>{attachments.items.length ? <Paperclip size={18} /> : <Plus size={20} />}</button>
        <input ref={fileInput} type="file" multiple hidden onChange={(event) => { attachments.add([...event.target.files]); event.target.value = ""; }} />
      </>}
      <div className="composer-chips">{chips}</div>
      {running
        ? <button type="button" className="send stop" aria-label={stopping ? "正在停止" : "停止运行"} title={stopping ? "正在停止" : "停止运行"} disabled={busy || stopping} onClick={() => { haptic("medium"); onStop(); }}><Square size={14} fill="currentColor" /></button>
        : <button type="submit" className="send" aria-label="发送" title="发送" disabled={!canSend}>{busy ? <LoaderCircle size={18} className="spin" /> : <ArrowUp size={19} strokeWidth={2.4} />}</button>}
    </div>
  </form>;
}
