export function deviceCommand(action, fields = {}) {
  const message = { action, ...fields };
  if (window.webkit?.messageHandlers?.cageDevices) {
    window.webkit.messageHandlers.cageDevices.postMessage(message);
    return true;
  } else if (window.CageDevices?.postMessage) {
    window.CageDevices.postMessage(JSON.stringify(message));
    return true;
  }
  return false;
}

// Light feedback on supported phones; the native shells ignore unknown styles.
export function haptic(style = "light") {
  return deviceCommand("haptic", { style });
}

// Hands a file to the system share sheet. Returns false outside the apps.
export function shareFile(name, mime, data) {
  return deviceCommand("share-file", { name, mime, data });
}
