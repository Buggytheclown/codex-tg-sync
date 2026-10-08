import { useState } from "react";
import type { FormEvent } from "react";
import type { Definition, Polling, Task } from "./model";
import { defaultPolling, emptyConfig, pollingProblem } from "./model";
import { ExecutionFields, Field, PollingFields } from "./forms";
import { PollingDiagnostics } from "./PollingDiagnostics";

function childWaiting(type: string, login: string): Polling {
  return {
    ...defaultPolling(),
    config: {
      login,
      pr_id: type === "cron" ? "" : "{{pr_id}}",
      author: type === "cron" ? "" : "{{author}}",
      conditions: ["new_commit", "all_answered"],
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
  ) => Promise<number>;
  onToggle?: (enabled: boolean, version: number) => Promise<number>;
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
      childWaiting(initial.type, initial.config.login || ""),
  );
  const [waitingConfigured, setWaitingConfigured] = useState(
    !!initial.action.child_definition?.polling,
  );
  const [message, setMessage] = useState("");
  const [trustedAuthors, setTrustedAuthors] = useState(
    (initial.action.initial_launch?.authors || []).join(", "),
  );
  const config = definition.config;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
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
    try {
      setVersion(await onSave(next, enabled, version, name));
      setMessage("Source saved. Changes apply to future tasks.");
    } catch (err) {
      setMessage((err as Error).message);
    }
  };
  return (
    <form className="source-editor" onSubmit={(e) => void submit(e)}>
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
      <section className="source-section">
        <h3>When to create a task</h3>
        {task ? (
          <div className="source-state">
            <span>
              {task.column === "polling" ? "Source enabled" : "Source paused"}
            </span>
            <button
              type="button"
              disabled={busy || !onToggle}
              onClick={async () => {
                try {
                  setVersion(
                    await onToggle!(!(task.column === "polling"), version),
                  );
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
        <PollingFields value={polling} onChange={setPolling} sourceOnly />
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
        {task && <PollingDiagnostics task={task} />}
        {!task && !enabled && (
          <p className="note">Starts paused. Enable it when ready.</p>
        )}
      </section>
      <section className="source-section">
        <h3>Task to create</h3>
        <p className="note">
          Changes apply to future tasks. Existing tasks keep their settings.
        </p>
        <Field
          label="Task title"
          hint="Source name is always prefixed. Schedules also include the scheduled date and time. PR: {{pr_title}}, {{pr_id}}, {{pr_url}}, {{author}}, {{login}}. Schedule: {{date}}, {{scheduled_at}}."
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
        <Field label="Prompt">
          <textarea
            required
            rows={5}
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
        <ExecutionFields
          compact
          value={config.execution}
          onChange={(execution) =>
            setDefinition({ ...definition, config: { ...config, execution } })
          }
        />
        <label className="check-line">
          <input
            type="checkbox"
            checked={repeat}
            onChange={(e) => {
              setRepeat(e.target.checked);
              if (e.target.checked && !waitingConfigured) {
                setWaiting(
                  childWaiting(polling.type, polling.config.login || ""),
                );
                setWaitingConfigured(true);
              } else if (e.target.checked && !waiting.config.login)
                setWaiting({
                  ...waiting,
                  config: {
                    ...waiting.config,
                    login: polling.config.login || "",
                  },
                });
            }}
          />
          Repeat work
        </label>
        {repeat && (
          <div className="child-settings">
            {polling.type === "cron" && (
              <p className="note">
                For scheduled repeat work, choose an existing PR by its numeric
                ID. PR placeholders are unavailable.
              </p>
            )}
            <PollingFields
              child
              variables={polling.type !== "cron"}
              value={waiting}
              onChange={setWaiting}
            />
            <Field
              label="Continuation prompt"
              hint="One instruction for continuing the same chat when its waiting condition is met."
            >
              <textarea
                required={waiting.action.type === "resume_task"}
                rows={4}
                value={config.continuation_prompt}
                onChange={(e) =>
                  setDefinition({
                    ...definition,
                    config: { ...config, continuation_prompt: e.target.value },
                  })
                }
              />
            </Field>
            <p className="note">
              Each created task starts with these waiting settings paused.
            </p>
          </div>
        )}
      </section>
      {message && (
        <p className="info-note" role="status">
          {message}
        </p>
      )}
      <button className="primary" disabled={busy}>
        {task ? "Save source" : "Create source"}
      </button>
    </form>
  );
}
