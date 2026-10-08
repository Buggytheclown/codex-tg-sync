import { cloneElement, useEffect, useId, useState } from "react";
import type { FormEvent, ReactElement } from "react";
import type { Polling, TaskConfig, ExecutionSettings } from "./model";
import { defaultPolling, pollingProblem } from "./model";
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactElement<{ id?: string; "aria-describedby"?: string }>;
  hint?: string;
}) {
  const id = useId();
  return (
    <div className="field">
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
}: {
  value: Polling;
  onChange: (p: Polling) => void;
  child?: boolean;
  variables?: boolean;
  sourceOnly?: boolean;
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
    <>
      <Field label="Polling type">
        <select value={value.type} onChange={(e) => changeType(e.target.value)}>
          {sourceOnly && (
            <>
              <option value="pr_review_source">PR review</option>
              <option value="pr_changes_source">
                My PRs requesting changes
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
            label="Cron schedule"
            hint="Daily or one weekday (0–6), for example 0 9 * * 1."
          >
            <input
              required
              value={value.config.cron || ""}
              onChange={(e) => cfg("cron", e.target.value)}
              placeholder="0 9 * * 1"
            />
          </Field>
          <Field label="Timezone">
            <input
              required
              value={value.config.timezone || ""}
              onChange={(e) => cfg("timezone", e.target.value)}
              placeholder="Europe/Paris"
            />
          </Field>
          <Field
            label="Maximum lateness"
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
              <Field label="PR ID">
                <input
                  required
                  value={value.config.pr_id || ""}
                  onChange={(e) => cfg("pr_id", e.target.value)}
                  placeholder={child && variables ? "{{pr_id}}" : "12345678"}
                />
              </Field>
              <Field
                label="PR author"
                hint="Optional; resolved from Arcanum when omitted."
              >
                <input
                  value={value.config.author || ""}
                  onChange={(e) => cfg("author", e.target.value)}
                  placeholder={child && variables ? "{{author}}" : "author"}
                />
              </Field>
              <label className="check-line">
                <input
                  type="checkbox"
                  checked={value.config.complete_on_merge === true}
                  onChange={(e) => cfg("complete_on_merge", e.target.checked)}
                />
                Move to Done when PR is merged
              </label>
              <p className="note">A merged PR completes this task without requesting a continuation.</p>
              <fieldset className="conditions">
                <legend>Wake when any condition becomes true</legend>
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
                  Conditions use ANY. An empty set of tracked comments never
                  counts as answered or resolved.
                </small>
              </fieldset>
            </>
          )}
          <Field label="Check every · seconds">
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
      {!sourceOnly && (
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
      {child && variables && (
        <p className="note">
          Copied to each child as inactive settings. Use {"{{pr_id}}"} and{" "}
          {"{{author}}"} for values from the source event.
        </p>
      )}
    </>
  );
}
export function PollingForm({
  initial,
  expectedVersion,
  onSave,
  activate = false,
  busy = false,
}: {
  initial?: Polling;
  expectedVersion: number;
  onSave: (p: Polling, activate: boolean, version: number) => Promise<{ version: number; polling?: Polling }>;
  activate?: boolean;
  busy?: boolean;
}) {
  const [value, setValue] = useState<Polling>(initial || defaultPolling());
  const [version, setVersion] = useState(expectedVersion);
  const [baseline, setBaseline] = useState(initial);
  // Runtime health/cursor changes are not editable polling fields.
  const editable = (p?: Polling) => JSON.stringify(p ? { type: p.type, mode: p.mode, config: p.config, action: p.action } : null);
  useEffect(() => {
    if (expectedVersion > version && editable(initial) === editable(baseline)) {
      setBaseline(initial); setVersion(expectedVersion);
    }
  }, [initial, baseline, expectedVersion, version]);
  const [error, setError] = useState("");
  const submit = async (e: FormEvent, enable: boolean) => {
    e.preventDefault();
    if (busy) return;
    if (version !== expectedVersion) {
      setError("Reload latest settings before saving. Your polling draft has been preserved.");
      return;
    }
    const problem = pollingProblem(value);
    if (problem) {
      setError(problem);
      return;
    }
    setError("");
    try {
      const saved = await onSave(value, enable, version);
      // A full reply can include a later concurrent edit after our commit.
      if (saved.version > version + (enable ? 2 : 1) && editable(saved.polling) !== editable(value)) {
        setVersion(version + 1);
        setBaseline(value);
        setError("This task changed after saving. Reload latest settings before saving again.");
      } else {
        setVersion(saved.version);
        setBaseline(saved.polling);
        setValue(current => editable(current) === editable(value) ? saved.polling || defaultPolling() : current);
      }
    } catch (err) {
      setError((err as Error).message);
    }
  };
  return (
    <form onSubmit={(e) => void submit(e, activate)}>
      <p className="note">⌘/Ctrl + Enter saves this section.</p>
      {version !== expectedVersion && <>
        <p className="note">This task changed. Reload latest settings before saving. Your polling draft has been preserved.</p>
        <button type="button" disabled={busy} onClick={() => { setValue(initial || defaultPolling()); setBaseline(initial); setVersion(expectedVersion); setError(""); }}>Reload latest polling</button>
      </>}
      <PollingFields value={value} onChange={setValue} />
      {error && (
        <p role="alert" className="error-note">
          {error}
        </p>
      )}
      <div className="form-actions">
        <button className="primary" disabled={busy}>
          {activate ? "Save & activate" : "Save polling"}
        </button>
        {!activate && (
          <button
            type="button"
            disabled={busy}
            onClick={(e) => void submit(e, true)}
          >
            Save & activate
          </button>
        )}
      </div>
      <p className="note">
        Only the Polling column activates these settings. TODO and Done pause
        checks.
      </p>
    </form>
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
