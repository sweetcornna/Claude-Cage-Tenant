import React, { createContext, useContext } from "react";
import { isSafetyCheckMessage, needsEnvironmentRestart } from "./safety-state.mjs";

export const SafetyChecklistContext = createContext(null);
// Provided only while a running environment can be restarted.
export const EnvironmentRestartContext = createContext(null);

export function SafetyMessage({ message }) {
  const openChecklist = useContext(SafetyChecklistContext);
  const restart = useContext(EnvironmentRestartContext);
  if (restart && needsEnvironmentRestart(message)) return <>
    <span className="safety-message-text">{message}</span>
    <button type="button" className="text-button safety-checklist-link" onClick={restart}>
      重启环境
    </button>
  </>;
  if (!openChecklist || !isSafetyCheckMessage(message)) return message;
  return <>
    <span className="safety-message-text">{message}</span>
    <button type="button" className="text-button safety-checklist-link" onClick={openChecklist}>
      查看检查清单
    </button>
  </>;
}
