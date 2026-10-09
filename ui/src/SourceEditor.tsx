import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import type { Definition, Polling, Task } from "./model";
import { defaultPolling, emptyConfig, pollingProblem } from "./model";
import { ExecutionFields, Field, PollingFields } from "./forms";
import { PollingDiagnostics } from "./PollingDiagnostics";
import { WorkflowStep, MergeCompletion } from "./Workflow";

function childWaiting(type: string): Polling {
  return {
    ...defaultPolling(),
    config: {
      complete_on_merge: type !== "cron",
      pr_id: type === "cron" ? "" : "{{pr_id}}",
      author: type === "cron" ? "" : "{{author}}",
      conditions: type === "pr_changes_source" ? ["all_resolved", "request_changes"] : ["new_commit", "all_answered"],
      interval_seconds: 60,
    },
    action: { type: "resume_task" },
  };
}
export function SourceEditor({
  task,
  busy,
  onSave,
  onToggle,
}: {
  task?: Task;
  busy: boolean;
  onSave: (
    polling: Polling,
    enabled: boolean,
    version: number,
    name: string,
  ) => Promise<Task>;
  onToggle?: (enabled: boolean, version: number) => Promise<Task>;
}) {
  const initial = task?.polling || defaultPolling("pr_review_source");
  const [polling, setPolling] = useState(initial);
  const [definition, setDefinition] = useState<Definition>(
    initial.action.child_definition || {
      title: "",
      description: "",
      config: emptyConfig(),
    },
  );
  const [enabled, setEnabled] = useState(task?.column === "polling");
  const [name, setName] = useState(task?.title || "");
  const [version, setVersion] = useState(task?.version || 0);
  const [repeat, setRepeat] = useState(
    !!initial.action.child_definition?.polling,
  );
  const [waiting, setWaiting] = useState(
    initial.action.child_definition?.polling ||
      childWaiting(initial.type),
  );
  const [waitingConfigured, setWaitingConfigured] = useState(
    !!initial.action.child_definition?.polling,
  );
  const [message, setMessage] = useState("");
  const [trustedAuthors, setTrustedAuthors] = useState(
    (initial.action.initial_launch?.authors || []).join(", "),
  );
  const [baseline, setBaseline] = useState(initial);
  const [saving, setSaving] = useState(false);
  const submitting = useRef(false);
  const busyNow = busy || saving;
  const stale = !!task && task.version > version;
  const editable = (p: Polling) => JSON.stringify({ type: p.type, mode: p.mode, config: p.config, action: p.action });
  useEffect(() => {
    if (task && task.version > version && editable(task.polling!) === editable(baseline)) { setVersion(task.version); setBaseline(task.polling!); }
  }, [task, version, baseline]);
  const config = definition.config;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busyNow || submitting.current) return;
    if (stale) { setMessage("Reload latest source before saving. Your draft has been preserved."); return; }
    const next: Polling = {
      ...polling,
      mode: "repeat",
      action: {
        type: "create_task",
        initial_launch:
          polling.action.initial_launch?.mode === "trusted_authors"
            ? {
                mode: "trusted_authors",
                authors: trustedAuthors
                  .split(",")
                  .map((author) => author.trim())
                  .filter(Boolean),
              }
            : polling.action.initial_launch || { mode: "manual" },
        child_definition: {
          ...definition,
          ...(repeat ? { polling: waiting } : { polling: undefined }),
        },
      },
    };
    const problem = pollingProblem(next);
    if (problem) {
      setMessage(problem);
      return;
    }
    setSaving(true); submitting.current = true;
    try {
      const saved = await onSave(next, enabled, version, name);
      const savedVersion = saved.version;
      if (savedVersion > version + 1 && editable(saved.polling!) !== editable(next)) {
        setVersion(version + 1); setBaseline(next);
        setMessage("This source changed after saving. Reload latest source before saving again.");
      } else {
        setVersion(savedVersion); setBaseline(saved.polling!);
        const authoritative = saved.polling!;
        const savedDefinition = authoritative.action.child_definition!;
        const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
        setPolling(current => same(current, polling) ? authoritative : current);
        setDefinition(current => ({
          ...savedDefinition,
          title: current.title === definition.title ? savedDefinition.title : current.title,
          description: current.description === definition.description ? savedDefinition.description : current.description,
          config: {
            initial_prompt: current.config.initial_prompt === config.initial_prompt ? savedDefinition.config.initial_prompt : current.config.initial_prompt,
            continuation_prompt: current.config.continuation_prompt === config.continuation_prompt ? savedDefinition.config.continuation_prompt : current.config.continuation_prompt,
            execution: Object.fromEntries(Object.entries(current.config.execution).map(([key, value]) => [key,
              value === config.execution[key as keyof typeof config.execution] ? savedDefinition.config.execution[key as keyof typeof config.execution] : value,
            ])) as typeof config.execution,
          },
        }));
        if (savedDefinition.polling) setWaiting(current => same(current, waiting) ? savedDefinition.polling! : current);
        setTrustedAuthors(current => current === trustedAuthors ? (authoritative.action.initial_launch?.authors || []).join(", ") : current);
        setMessage("Source saved. Changes apply to future tasks.");
      }
    } catch (err) {
      setMessage((err as Error).message);
    } finally { setSaving(false); submitting.current = false; }
  };
  return (
    <form className="source-editor workflow-editor" onSubmit={(e) => void submit(e)}>
      <h2>Workflow steps</h2>
      {!task && (
        <Field label="Source name">
          <input
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="PR review"
          />
        </Field>
      )}
      <WorkflowStep number={1} title="When">
        {task ? (
          <div className="source-state">
            <span>
              {task.column === "polling" ? "Source enabled" : "Source paused"}
            </span>
            <button
              type="button"
              disabled={busyNow || stale || !onToggle}
              onClick={async () => {
                try {
                  const saved = await onToggle!(!(task.column === "polling"), version);
                  if (editable(saved.polling!) !== editable(baseline)) {
                    setVersion(version + 1);
                    setMessage("This source changed. Reload latest source before saving again.");
                    return;
                  }
                  setVersion(saved.version);
                  setMessage(
                    task.column === "polling"
                      ? "Source paused."
                      : "Source enabled.",
                  );
                } catch (err) {
                  setMessage((err as Error).message);
                }
              }}
            >
              {task.column === "polling" ? "Pause source" : "Enable source"}
            </button>
          </div>
        ) : (
          <label className="check-line">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            Source enabled
          </label>
        )}
        <p className="note">
          Sources create tasks in Inbox. Initial launch follows this source’s
          policy; continuations require Telegram Start.
        </p>
        <PollingFields workflow value={polling} onChange={setPolling} sourceOnly />
        {task && <details className="form-section"><summary>Polling status</summary><PollingDiagnostics task={task} /></details>}
        {!task && !enabled && (
          <p className="note">Starts paused. Enable it when ready.</p>
        )}
      </WorkflowStep>
      <WorkflowStep number={2} title="Create task">
        <p className="note">
          Changes apply to future tasks. Existing tasks keep their settings.
        </p>
        <Field
          label="Task title"
          hint="Prefixed by source name; schedules also include the scheduled date and time."
        >
          <input
            required
            value={definition.title}
            onChange={(e) =>
              setDefinition({ ...definition, title: e.target.value })
            }
            placeholder="Review PR {{pr_id}}"
          />
        </Field>
        <Field label="Description">
          <textarea
            rows={2}
            value={definition.description}
            onChange={(e) =>
              setDefinition({ ...definition, description: e.target.value })
            }
          />
        </Field>
        <Field label="Initial prompt">
          <textarea
            required
            rows={3}
            value={config.initial_prompt}
            onChange={(e) =>
              setDefinition({
                ...definition,
                config: { ...config, initial_prompt: e.target.value },
              })
            }
            placeholder="What should Codex do?"
          />
        </Field>
        <details className="form-section"><summary>Prompt variables</summary><p className="note section-content">PR: {"{{pr_title}}, {{pr_id}}, {{pr_url}}, {{author}}, {{login}}"}. Schedule: {"{{date}}, {{scheduled_at}}"}.</p></details>
        <details className="form-section"><summary>Working directory · Model · Permissions</summary>
        <ExecutionFields
          compact
          value={config.execution}
          onChange={(execution) =>
            setDefinition({ ...definition, config: { ...config, execution } })
          }
        />
        </details>
        <Field
          label="Initial launch"
          hint="A Telegram request card appears before automatic start. This applies only to new tasks; continuations require Start."
        >
          <select
            value={polling.action.initial_launch?.mode || "manual"}
            onChange={(e) =>
              setPolling({
                ...polling,
                action: {
                  ...polling.action,
                  initial_launch: {
                    mode: e.target.value as "manual" | "trusted_authors" | "auto",
                    authors: polling.action.initial_launch?.authors || [],
                  },
                },
              })
            }
          >
            <option value="manual">Manual · Telegram Start</option>
            {polling.type !== "cron" && (
              <option value="trusted_authors">
                Automatic · trusted PR authors
              </option>
            )}
            <option value="auto">Automatic · every new task</option>
          </select>
        </Field>
        {polling.action.initial_launch?.mode === "trusted_authors" && (
          <Field
            label="Trusted PR authors"
            hint="Comma-separated Arcanum logins. Other authors require Telegram Start."
          >
            <input
              value={trustedAuthors}
              onChange={(e) => setTrustedAuthors(e.target.value)}
            />
          </Field>
        )}
        <label className="check-line">
          <input
            type="checkbox"
            checked={repeat}
            onChange={(e) => {
              setRepeat(e.target.checked);
              if (e.target.checked && !waitingConfigured) {
                setWaiting(
                  childWaiting(polling.type),
                );
                setWaitingConfigured(true);
              }
            }}
          />
          Waiting settings for new tasks
        </label>
      </WorkflowStep>
        {repeat && (
          <WorkflowStep number={3} title="Waiting settings for new tasks" tone="future">
            {polling.type === "cron" && (
              <p className="note">
                For scheduled repeat work, choose an existing PR by its numeric
                ID. PR placeholders are unavailable.
              </p>
            )}
            <PollingFields
              child workflow
              variables={polling.type !== "cron"}
              value={waiting}
              onChange={setWaiting}
            />
            <Field label="Action"><select value={waiting.action.type} onChange={e => setWaiting({ ...waiting, action: { ...waiting.action, type: e.target.value } })}>
              <option value="return_task">Return to Inbox</option><option value="resume_task">Request continuation</option>
            </select></Field>
            <Field
              label="Continuation prompt"
              hint="One instruction for continuing the same chat when its waiting condition is met."
            >
              <textarea
                required={waiting.action.type === "resume_task"}
                rows={3}
                value={config.continuation_prompt}
                onChange={(e) =>
                  setDefinition({
                    ...definition,
                    config: { ...config, continuation_prompt: e.target.value },
                  })
                }
              />
            </Field>
            <MergeCompletion value={waiting} onChange={setWaiting} />
            <p className="note">Copied. Activate manually later.</p>
          </WorkflowStep>
        )}
      {message && (
        <p className="info-note" role="status">
          {message}
        </p>
      )}
      {stale && <div className="readiness-banner"><p>This source changed. Your draft has been preserved.</p><button type="button" disabled={busyNow} onClick={() => {
        const latest = task!.polling!; const child = latest.action.child_definition!;
        setPolling(latest); setBaseline(latest); setDefinition(child); setWaiting(child.polling || childWaiting(latest.type));
        setRepeat(!!child.polling); setWaitingConfigured(!!child.polling); setTrustedAuthors((latest.action.initial_launch?.authors || []).join(", ")); setVersion(task!.version); setMessage("");
      }}>Reload latest source</button></div>}
      <div className="source-footer workflow-footer"><p className="future-notice">Changes apply only to future tasks</p>
      <button className="primary" disabled={busyNow}>
        {task ? "Save source" : "Create source"}
      </button></div>
    </form>
  );
}
