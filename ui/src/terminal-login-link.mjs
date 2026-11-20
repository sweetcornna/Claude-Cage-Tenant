// Match printed links in xterm's wrapped buffer. OSC 8 links also use this
// exact destination check before opening the guest's private login tab.
export const claudeLoginLink = /https:\/\/(?:claude\.ai|claude\.com|console\.anthropic\.com|platform\.claude\.com|platform\.anthropic\.com)\/(?:cai\/)?oauth\/authorize\/?\?[^\s"'<>\\\x1b]+/i;

export function isClaudeLoginLink(uri) {
  const match = claudeLoginLink.exec(uri);
  return uri.length <= 8192 && match?.[0] === uri;
}

// Codex device login prints a fixed page; the one-time code is entered there.
// Opening it in the user's own browser carries no token or callback.
export const codexDeviceLink = /https:\/\/auth\.openai\.com\/codex\/device(?![\w/?#.-])/;

export function isCodexDeviceLink(uri) {
  return uri === "https://auth.openai.com/codex/device";
}
