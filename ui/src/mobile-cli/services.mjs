import { mobileRequest } from "./transport.mjs";

const post = (path, body, options = {}) => mobileRequest(path, { ...options, body });
const terminalPath = (id) => "/terminals/" + encodeURIComponent(id);

export const environment = {
  status: (options) => mobileRequest("/environment", options),
  run: (action) => post("/environment/" + action, {}),
  proxy: (options) => mobileRequest("/environment/proxy", options),
  select: (nodeID) => post("/environment/proxy/select", { node_id: nodeID }),
  delay: (nodeID, options) => post("/environment/proxy/delay", { node_id: nodeID }, options),
};

export const terminals = {
  list: (options) => mobileRequest("/terminals", options),
  create: (kind, workspace) => post("/terminals", { kind, workspace }),
  output: (id, after, options) => mobileRequest(terminalPath(id) + "/output?after=" + after, options),
  input: (id, data) => post(terminalPath(id) + "/input", { data }),
  resize: (id, cols, rows) => post(terminalPath(id) + "/resize", { cols, rows }),
  close: (id) => post(terminalPath(id) + "/close", {}),
};

export function bytesToBase64(bytes) {
  let text = "";
  for (let i = 0; i < bytes.length; i += 0x2000) text += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x2000));
  return btoa(text);
}
export function base64ToBytes(data) {
  const text = atob(data || "");
  const bytes = new Uint8Array(text.length);
  for (let i = 0; i < text.length; i++) bytes[i] = text.charCodeAt(i);
  return bytes;
}

const uploadChunk = 48 << 10;
export const maxUpload = 100 << 20;
export const maxPreview = 8 << 20;

export const files = {
  list: (project, path, options) => post("/files/list", { project, path }, options),
  mkdir: (project, path) => post("/files/mkdir", { project, path }),
  // Reads up to limit bytes; returns {size, mime, bytes, complete}.
  async read(project, path, { limit = maxPreview, signal, onProgress } = {}) {
    let offset = 0, version = "", size = 0, mime = "";
    const parts = [];
    do {
      const chunk = await post("/files/read", { project, path, offset, version }, { signal });
      size = chunk.size; mime = chunk.mime || mime; version = chunk.version;
      const bytes = base64ToBytes(chunk.data);
      if (!bytes.length) break;
      parts.push(bytes); offset += bytes.length;
      onProgress?.(Math.min(offset, size), size);
    } while (offset < size && offset < limit);
    const bytes = new Uint8Array(offset);
    let at = 0;
    for (const part of parts) { bytes.set(part, at); at += part.length; }
    return { size, mime, bytes, complete: offset >= size };
  },
  async upload(project, dir, file, { name = file.name, overwrite = false, signal, onProgress } = {}) {
    if (file.size > maxUpload) throw new Error("文件超过 100 MB，请在电脑上传。");
    const data = new Uint8Array(await file.arrayBuffer());
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", data));
    const sha256 = [...digest].map((b) => b.toString(16).padStart(2, "0")).join("");
    const { id } = await post("/files/upload/start", { project, dir, name, size: data.length, overwrite }, { signal });
    try {
      for (let offset = 0; offset < data.length; offset += uploadChunk) {
        if (signal?.aborted) throw new DOMException("已取消", "AbortError");
        await post("/files/upload/chunk", { id, offset, data: bytesToBase64(data.subarray(offset, offset + uploadChunk)) }, { signal });
        onProgress?.(Math.min(offset + uploadChunk, data.length), data.length);
      }
      return await post("/files/upload/finish", { id, sha256 }, { signal });
    } catch (error) {
      post("/files/upload/abort", { id }).catch(() => {});
      throw error;
    }
  },
};

export const attachmentDir = ".super-cage/attachments";

export function formatSize(size) {
  if (!(size >= 0)) return "";
  if (size < 1024) return size + " B";
  const units = ["KB", "MB", "GB"];
  let value = size / 1024, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return (value >= 10 ? Math.round(value) : value.toFixed(1)) + " " + units[unit];
}
