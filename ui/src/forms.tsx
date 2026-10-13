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
export const conditionLabels: Record<string, string> = {
  new_commit: "Any new commit",
  all_answered: "All tracked comments answered",
  all_resolved: "All tracked issues resolved",
  request_changes: "Transitions to request changes",
};
export function PollingFields({
  value,
  onChange,
  child = false,
  variables = true,
  sourceOnly = false,
  workflow = false,
}: {
  value: Polling;
  onChange: (p: Polling) => void;
  child?: boolean;
  variables?: boolean;
  sourceOnly?: boolean;
  workflow?: boolean;
}) {
  const cfg = (key: string, v: unknown) =>
    onChange({ ...value, config: { ...value.config, [key]: v } });
  const changeType = (type: string) => {
    const p = defaultPolling(type);
    onChange({
      ...p,
      config: p.config,
      action: {
        ...p.action,
        child_definition: value.action.child_definition,
        initial_launch:
          type === "cron" &&
          value.action.initial_launch?.mode === "trusted_authors"
            ? { mode: "manual" }
            : value.action.initial_launch,
      },
    });
  };
  const source =
    value.type === "pr_review_source" ||
    value.type === "pr_changes_source" ||
    value.type === "cron";
  return (
    <div className="polling-fields">
      <Field label="Polling type" className="polling-type">
        <select value={value.type} onChange={(e) => changeType(e.target.value)}>
          {sourceOnly && (
            <>
              <option value="pr_review_source">PR review</option>
              <option value="pr_changes_source">
                PR feedback
              </option>
              <option value="cron">Cron schedule</option>
            </>
          )}
          {!sourceOnly && <option value="pr_wait">Wait on one PR</option>}
        </select>
      </Field>
      {value.type === "cron" ? (
        <>
          <Field
            label="Cron schedule" className="polling-cron"
            hint="Daily or one weekday (0–6), for example 0 9 * * 1."
          >
            <input
              required
              value={value.config.cron || ""}
              onChange={(e) => cfg("cron", e.target.value)}
              placeholder="0 9 * * 1"
            />
          </Field>
          <Field label="Timezone" className="polling-timezone">
            <input
              required
              value={value.config.timezone || ""}
              onChange={(e) => cfg("timezone", e.target.value)}
              placeholder="Europe/Paris"
            />
          </Field>
          <Field
            label="Maximum lateness" className="polling-lateness"
            hint="Optional. Skip an occurrence older than this duration."
          >
            <input
              value={value.config.max_lateness || ""}
              onChange={(e) => cfg("max_lateness", e.target.value)}
              placeholder="5m"
            />
          </Field>
        </>
      ) : (
        <>
          {value.type === "pr_wait" && (
            <>
              <div className="pr-id-row"><Field label="PR ID" hint={child && variables && value.config.pr_id === "{{pr_id}}" ? "From detected PR" : undefined}>
                <input
                  required
                  inputMode={child && variables ? undefined : "numeric"}
                  pattern={child ? undefined : "[1-9][0-9]*"}
                  readOnly={child && variables && value.config.pr_id === "{{pr_id}}"}
                  value={value.config.pr_id || ""}
                  onChange={(e) => cfg("pr_id", e.target.value)}
                  placeholder={child && variables ? "{{pr_id}}" : "12345678"}
                />
              </Field>
              {workflow && !child && /^[1-9]\d*$/.test(value.config.pr_id || "") && <a className="pr-target-link" href={`https://a.yandex-team.ru/review/${value.config.pr_id}`} target="_blank" rel="noreferrer" aria-label={`#${value.config.pr_id} Open PR`}>#{value.config.pr_id} ↗ <span>Open PR</span></a>}
              </div>
              {<details className="polling-author"><summary>PR author (optional)</summary><Field
                label="PR author"
                hint="Optional; resolved from Arcanum when omitted."
              >
                <input
                  value={value.config.author || ""}
                  onChange={(e) => cfg("author", e.target.value)}
                  placeholder={child && variables ? "{{author}}" : "author"}
                />
              </Field></details>}
              {!workflow && <><label className="check-line">
                <input
                  type="checkbox"
                  checked={value.config.complete_on_merge === true}
                  onChange={(e) => cfg("complete_on_merge", e.target.checked)}
                />
                Move to Done when PR is merged
              </label>
              <p className="note">A merged PR completes this task without requesting a continuation.</p></>}
              <fieldset className="conditions">
                <legend>Conditions (ANY)</legend>
                {Object.entries(conditionLabels).map(([key, label]) => (
                  <label key={key}>
                    <input
                      type="checkbox"
                      checked={value.config.conditions?.includes(key) || false}
                      onChange={(e) =>
                        cfg(
                          "conditions",
                          e.target.checked
                            ? [...(value.config.conditions || []), key]
                            : (value.config.conditions || []).filter(
                                (v) => v !== key,
                              ),
                        )
                      }
                    />
                    <span>{label}</span>
                  </label>
                ))}
                <small>
                  Any selected condition wakes this task. Empty issue sets do not count.
                </small>
              </fieldset>
            </>
          )}
          <Field label="Check every · seconds" className="polling-interval">
            <input
              type="number"
              min="10"
              max="86400"
              required
              value={value.config.interval_seconds || 60}
              onChange={(e) => cfg("interval_seconds", Number(e.target.value))}
            />
          </Field>
        </>
      )}
      {!sourceOnly && !workflow && (
        <div>
          <Field label="Action">
            <select
              value={value.action.type}
              disabled={source}
              onChange={(e) =>
                onChange({ ...value, action: { type: e.target.value } })
              }
            >
              <option value="return_task">Return to Inbox</option>
              <option value="resume_task">Request continuation</option>
            </select>
          </Field>
        </div>
      )}
      {child && variables && !workflow && (
        <p className="note">
          Copied to each child as inactive settings. Use {"{{pr_id}}"} and{" "}
          {"{{author}}"} for values from the source event.
        </p>
      )}
    </div>
  );
}
export function ExecutionFields({
  value,
  onChange,
  compact = false,
}: {
  value: ExecutionSettings;
  onChange: (value: ExecutionSettings) => void;
  compact?: boolean;
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
      {compact ? (
        <details className="form-section">
          <summary>Permissions</summary>{" "}
          <Field label="Approval policy">
            <select
              value={value.approval_policy}
              onChange={(e) => execution("approval_policy", e.target.value)}
            >
              {["on-request", "untrusted", "never"].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </Field>
          <Field label="Approval reviewer">
            <select
              value={value.approvals_reviewer}
              onChange={(e) => execution("approvals_reviewer", e.target.value)}
            >
              {["auto_review", "user"].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </Field>
          <Field label="Sandbox">
            <select
              value={value.sandbox_mode}
              onChange={(e) => execution("sandbox_mode", e.target.value)}
            >
              {["workspace-write", "read-only", "danger-full-access"].map(
                (x) => (
                  <option key={x}>{x}</option>
                ),
              )}
            </select>
          </Field>
        </details>
      ) : (
        <>
          {" "}
          <Field label="Approval policy">
            <select
              value={value.approval_policy}
              onChange={(e) => execution("approval_policy", e.target.value)}
            >
              {["on-request", "untrusted", "never"].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </Field>
          <Field label="Approval reviewer">
            <select
              value={value.approvals_reviewer}
              onChange={(e) => execution("approvals_reviewer", e.target.value)}
            >
              {["auto_review", "user"].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </Field>
          <Field label="Sandbox">
            <select
              value={value.sandbox_mode}
              onChange={(e) => execution("sandbox_mode", e.target.value)}
            >
              {["workspace-write", "read-only", "danger-full-access"].map(
                (x) => (
                  <option key={x}>{x}</option>
                ),
              )}
            </select>
          </Field>
        </>
      )}
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
