// Plain-language summaries of GET /api/mobile/environment.
export function environmentSummary(env) {
  if (!env) return { tone: "unknown", title: "正在读取环境…", short: "读取中", doodle: "moon" };
  const job = env.job?.running ? env.job : null;
  if (job) {
    const verb = { start: "正在启动", stop: "正在停止", restart: "正在重启", proxy: "正在切换节点" }[job.kind] || "正在处理";
    return { tone: "busy", title: verb + "…", detail: job.message || "", short: verb, doodle: job.kind === "stop" ? "moon" : "sun", busy: true };
  }
  if (env.ready) {
    const mode = env.network?.mode;
    if (mode === "open") return { tone: "ok", title: "环境运行中", detail: "外网已开启" + (env.network?.exit_ip ? " · 出口 " + env.network.exit_ip : ""), short: "运行中", doodle: "sun" };
    if (mode === "checking") return { tone: "busy", title: "环境运行中", detail: "正在检测网络…", short: "检测网络", doodle: "sun" };
    return { tone: "warn", title: "环境运行中", detail: "外网已关闭" + (env.network?.reason ? "：" + env.network.reason : ""), short: "外网关闭", doodle: "sun" };
  }
  if (env.vm_running) return { tone: "busy", title: "环境准备中", detail: env.job?.message || "", short: "准备中", doodle: "sun" };
  return { tone: "off", title: "环境已停止", detail: env.job?.error || "启动后才能对话、使用终端和文件。", short: "已停止", doodle: "moon" };
}

export function jobFailed(env, jobID) {
  return env?.job && env.job.id === jobID && !env.job.running && !!env.job.error;
}
