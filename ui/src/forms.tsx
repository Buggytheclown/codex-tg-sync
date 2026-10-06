import { cloneElement, useId, useState } from "react";
import type { FormEvent, ReactElement } from "react";
import type { Polling, TaskConfig, Template } from "./model";
import { defaultPolling, emptyConfig, pollingProblem } from "./model";
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
  const execution = (key: keyof TaskConfig["execution"], text: string) =>
    onChange({ ...value, execution: { ...value.execution, [key]: text } });
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
        <div className="section-content">
          <Field label="Working directory">
            <input
              value={value.execution.cwd}
              onChange={(e) => execution("cwd", e.target.value)}
              placeholder="Use daemon default"
            />
          </Field>
          <div className="field-pair">
            <Field label="Model">
              <input
                value={value.execution.model}
                onChange={(e) => execution("model", e.target.value)}
                placeholder="Default"
              />
            </Field>
            <Field label="Reasoning">
              <select
                value={value.execution.reasoning_effort}
                onChange={(e) => execution("reasoning_effort", e.target.value)}
              >
                <option value="">Default</option>
                {["low", "medium", "high", "xhigh", "max"].map((x) => (
                  <option key={x}>{x}</option>
                ))}
              </select>
            </Field>
          </div>
          <Field label="Approval policy">
            <select
              value={value.execution.approval_policy}
              onChange={(e) => execution("approval_policy", e.target.value)}
            >
              {["on-request", "untrusted", "never"].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </Field>
          <Field label="Approval reviewer">
            <select
              value={value.execution.approvals_reviewer}
              onChange={(e) => execution("approvals_reviewer", e.target.value)}
            >
              {["auto_review", "user"].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </Field>
          <Field label="Sandbox">
            <select
              value={value.execution.sandbox_mode}
              onChange={(e) => execution("sandbox_mode", e.target.value)}
            >
              {["workspace-write", "read-only", "danger-full-access"].map(
                (x) => (
                  <option key={x}>{x}</option>
                ),
              )}
            </select>
          </Field>
        </div>
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
  templates,
  child = false,
  sourceOnly = false,
}: {
  value: Polling;
  onChange: (p: Polling) => void;
  templates: Template[];
  child?: boolean;
  sourceOnly?: boolean;
}) {
  const cfg = (key: string, v: unknown) =>
    onChange({ ...value, config: { ...value.config, [key]: v } });
  const changeType = (type: string) => {
    const p = defaultPolling(type);
    onChange({
      ...p,
      config: { ...p.config, login: value.config.login || "" },
      action: { ...p.action, template_id: value.action.template_id },
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
          {!child && (
            <>
              <option value="pr_review_source">Assigned PRs</option>
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
          <Field
            label="Your Arcanum login"
            hint={
              source ? undefined : "Needed to track your answered comments."
            }
          >
            <input
              required={
                source || value.config.conditions?.includes("all_answered")
              }
              value={value.config.login || ""}
              onChange={(e) => cfg("login", e.target.value)}
              placeholder="reviewer"
            />
          </Field>
          {value.type === "pr_wait" && (
            <>
              <Field label="PR ID">
                <input
                  required
                  value={value.config.pr_id || ""}
                  onChange={(e) => cfg("pr_id", e.target.value)}
                  placeholder={child ? "{{pr_id}}" : "12345678"}
                />
              </Field>
              <Field
                label="PR author"
                hint="Optional; resolved from Arcanum when omitted."
              >
                <input
                  value={value.config.author || ""}
                  onChange={(e) => cfg("author", e.target.value)}
                  placeholder={child ? "{{author}}" : "author"}
                />
              </Field>
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
      <div className="field-pair">
        <Field label="Mode">
          <select
            value={value.mode}
            disabled={source}
            onChange={(e) => onChange({ ...value, mode: e.target.value })}
          >
            <option value="once">Once</option>
            <option value="repeat">Repeat</option>
          </select>
        </Field>
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
            <option value="create_task">Create task</option>
          </select>
        </Field>
      </div>
      {value.action.type === "create_task" && (
        <Field
          label="Template for new tasks"
          hint="The current template is copied when a new task is created."
        >
          <select
            required
            value={value.action.template_id || ""}
            onChange={(e) =>
              onChange({
                ...value,
                action: { ...value.action, template_id: e.target.value },
              })
            }
          >
            <option value="">Select a template</option>
            {templates.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </Field>
      )}
      {child && (
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
  templates,
  onSave,
  activate = false,
  busy = false,
}: {
  initial?: Polling;
  expectedVersion: number;
  templates: Template[];
  onSave: (p: Polling, activate: boolean, version: number) => Promise<number>;
  activate?: boolean;
  busy?: boolean;
}) {
  const [value, setValue] = useState<Polling>(initial || defaultPolling());
  const [version, setVersion] = useState(expectedVersion);
  const [error, setError] = useState("");
  const submit = async (e: FormEvent, enable: boolean) => {
    e.preventDefault();
    const problem = pollingProblem(value);
    if (problem) {
      setError(problem);
      return;
    }
    setError("");
    try {
      const savedVersion = await onSave(value, enable, version);
      setVersion(savedVersion);
    } catch (err) {
      setError((err as Error).message);
    }
  };
  return (
    <form onSubmit={(e) => void submit(e, activate)}>
      <PollingFields value={value} onChange={setValue} templates={templates} />
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
export const blankDefinition = () => ({
  title: "",
  description: "",
  config: emptyConfig(),
});
