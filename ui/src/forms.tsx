import { cloneElement, useEffect, useId, useRef, useState } from "react";
import type { FormEvent, ReactElement } from "react";
import type { Polling, TaskConfig, ExecutionSettings, Task, TaskCard } from "./model";
import { defaultPolling, parseThreadID } from "./model";
export function Field({
  label,
  children,
  hint,
  className = "",
}: {
  label: string;
  children: ReactElement<{ id?: string; "aria-describedby"?: string }>;
  hint?: string;
  className?: string;
}) {
  const id = useId();
  return (
    <div className={`field ${className}`}>
      <label htmlFor={id}>{label}</label>
      {cloneElement(children, {
        id,
        "aria-describedby": hint ? `${id}-hint` : undefined,
      })}
      {hint && <small id={`${id}-hint`}>{hint}</small>}
    </div>
  );
}
export function PromptFields({
  value,
  onChange,
}: {
  value: TaskConfig;
  onChange: (config: TaskConfig) => void;
}) {
  const update = (key: keyof TaskConfig, text: string) =>
    onChange({ ...value, [key]: text });
  return (
    <>
      <Field label="Initial prompt" hint="Used for the first launch request.">
        <textarea
          rows={5}
          value={value.initial_prompt}
          onChange={(e) => update("initial_prompt", e.target.value)}
          placeholder="What should Codex do?"
        />
      </Field>
      <Field
        label="Continuation prompt"
        hint="One prompt for resuming this same chat after a waiting condition."
      >
        <textarea
          rows={4}
          value={value.continuation_prompt}
          onChange={(e) => update("continuation_prompt", e.target.value)}
          placeholder="Check the new revision and follow up on the feedback."
        />
      </Field>
      <details className="form-section">
        <summary>Execution settings</summary>
        <ExecutionFields
          value={value.execution}
          onChange={(execution) => onChange({ ...value, execution })}
        />
      </details>
    </>
  );
}
export function PollingFields({ value, onChange }: { value: Polling; onChange: (p: Polling) => void }) {
  const cfg = (key: string, v: unknown) => onChange({ ...value, config: { ...value.config, [key]: v } });
  const changeType = (type: string) => {
    const next = defaultPolling(type);
    onChange({ ...next, action: {
      ...next.action, column: value.action.column,
      child_definition: value.action.child_definition,
      initial_launch: type === "cron" && value.action.initial_launch?.mode === "trusted_authors"
        ? { mode: "manual" } : value.action.initial_launch,
    } });
  };
  return <div className="polling-fields">
    <Field label="Polling type" className="polling-type"><select value={value.type} onChange={e => changeType(e.target.value)}>
      <option value="pr_review_source">PR review</option><option value="pr_changes_source">PR feedback</option><option value="cron">Cron schedule</option>
    </select></Field>
    {value.type === "cron" ? <>
      <Field label="Cron schedule" className="polling-cron" hint="Daily or one weekday (0–6), for example 0 9 * * 1."><input required value={value.config.cron || ""} onChange={e => cfg("cron", e.target.value)} placeholder="0 9 * * 1" /></Field>
      <Field label="Timezone" className="polling-timezone"><input required value={value.config.timezone || ""} onChange={e => cfg("timezone", e.target.value)} placeholder="Europe/Paris" /></Field>
      <Field label="Maximum lateness" className="polling-lateness" hint="Optional. Skip an occurrence older than this duration."><input value={value.config.max_lateness || ""} onChange={e => cfg("max_lateness", e.target.value)} placeholder="5m" /></Field>
    </> : <Field label="Check every · seconds" className="polling-interval"><input type="number" min="10" max="86400" required value={value.config.interval_seconds || 60} onChange={e => cfg("interval_seconds", Number(e.target.value))} /></Field>}
  </div>;
}
export function ExecutionFields({
  value,
  onChange,
}: {
  value: ExecutionSettings;
  onChange: (value: ExecutionSettings) => void;
}) {
  const execution = (key: keyof ExecutionSettings, text: string) =>
    onChange({ ...value, [key]: text });
  return (
    <div className="section-content">
      {" "}
      <Field label="Working directory">
        <input
          value={value.cwd}
          onChange={(e) => execution("cwd", e.target.value)}
          placeholder="Use daemon default"
        />
      </Field>
      <div className="field-pair">
        <Field label="Model">
          <input
            value={value.model}
            onChange={(e) => execution("model", e.target.value)}
            placeholder="Default"
          />
        </Field>
        <Field label="Reasoning">
          <select
            value={value.reasoning_effort}
            onChange={(e) => execution("reasoning_effort", e.target.value)}
          >
            <option value="">Default</option>
            {["low", "medium", "high", "xhigh", "max"].map((x) => (
              <option key={x}>{x}</option>
            ))}
          </select>
        </Field>
      </div>
    </div>
  );
}

export function TaskChatForm({ task, busy, onLink, inline = false, onCancel, onSaved, submitLabel = "Link chat" }: { task: Task | TaskCard; busy: boolean; onLink: (threadID: string, version: number) => Promise<Task>; inline?: boolean; onCancel?: () => void; onSaved?: () => void; submitLabel?: string }) {
  const [baseline, setBaseline] = useState(task);
  const [chat, setChat] = useState(task.thread_id || "");
  const draft = useRef(chat);
  const [error, setError] = useState("");
  useEffect(() => {
    if (task.version > baseline.version && task.thread_id === baseline.thread_id) setBaseline(task);
  }, [task, baseline]);
  const stale = task.version !== baseline.version || task.thread_id !== baseline.thread_id;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (stale) { setError("The chat binding changed. Reload latest chat before saving; your draft is preserved."); return; }
    const threadID = parseThreadID(chat);
    if (!threadID) { setError("Enter a Codex chat ID or codex://threads/ link."); return; }
    setError("");
    try {
      const saved = await onLink(threadID, baseline.version);
      if (saved.thread_id !== threadID) { setError("The chat binding changed after saving. Reload latest chat before saving again."); return; }
      setBaseline(saved);
      if (draft.current === chat) onSaved?.();
    } catch (err) { setError((err as Error).message); }
  };
  return <form onSubmit={(e) => void submit(e)}>
    <Field label={inline ? `Codex chat for ${task.title}` : "Codex chat"} hint="Paste an existing chat ID or codex://threads/ link.">
      <input required autoFocus={inline} value={chat} onChange={(e) => { draft.current = e.target.value; setChat(e.target.value); }} />
    </Field>
    {error && <p role="alert" className="error-note">{error}</p>}
    {stale && <button type="button" disabled={busy} onClick={() => { setBaseline(task); setChat(task.thread_id || ""); draft.current = task.thread_id || ""; setError(""); }}>Reload latest chat</button>}
    <button disabled={busy}>{submitLabel}</button>
    {inline && onCancel && <button type="button" disabled={busy} onClick={onCancel}>Cancel</button>}
  </form>;
}
