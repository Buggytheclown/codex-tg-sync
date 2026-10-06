import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "./api";
import type {
  Column,
  Definition,
  Polling,
  Task,
  TaskEvent,
  Template,
} from "./model";
import {
  codexURL,
  columns,
  defaultPolling,
  emptyConfig,
  executionLabel,
  formatTime,
  isSource,
  labels,
  pollingLabel,
  pollingProblem,
  safeExternalURL,
  parseThreadID,
} from "./model";
import {
  Field,
  PollingFields,
  PollingForm,
  PromptFields,
  blankDefinition,
  conditionLabels,
} from "./forms";

type Panel = "details" | "new-task" | "new-source" | "add-chat" | "templates";
type Tab = "overview" | "settings" | "polling" | "activity";
export function TaskCard({
  task,
  selected,
  onSelect,
}: {
  task: Task;
  selected: boolean;
  onSelect: () => void;
}) {
  const execution = executionLabel(task);
  return (
    <button
      className={`task-card ${selected ? "selected" : ""}`}
      onClick={onSelect}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData("text/task-id", task.id);
        e.dataTransfer.effectAllowed = "move";
      }}
      aria-pressed={selected}
    >
      <span className="card-top">
        <span className="eyebrow">
          {task.thread_id ? "CHAT" : task.source_key ? "AUTOMATION" : "TASK"}
        </span>
        <span className="card-grip" aria-hidden="true">
          ⠿
        </span>
      </span>
      <strong>{task.title}</strong>
      {task.description && (
        <span className="card-description">{task.description}</span>
      )}
      <span className="card-meta">
        <span
          className={`poll-chip ${task.column === "polling" ? "active" : ""}`}
        >
          {pollingLabel(task)}
        </span>
        {execution && (
          <span className={`execution ${execution.toLowerCase()}`}>
            <i />
            {execution}
          </span>
        )}
      </span>
      {task.requests?.some((r) =>
        ["pending_approval", "starting"].includes(r.status),
      ) && <span className="request-hint">Launch request · Telegram</span>}
    </button>
  );
}
export function App() {
  const [tasks, setTasks] = useState<Task[]>([]),
    [templates, setTemplates] = useState<Template[]>([]);
  const [selectedID, setSelectedID] = useState(
      () => new URLSearchParams(window.location.search).get("task") || "",
    ),
    [panel, setPanel] = useState<Panel>("details"),
    [tab, setTab] = useState<Tab>("overview");
  const [collapsed, setCollapsed] = useState(false),
    [query, setQuery] = useState(""),
    [error, setError] = useState(""),
    [info, setInfo] = useState("");
  const [activationRequested, setActivationRequested] = useState(false);
  const [connection, setConnection] = useState("Connecting"),
    [loaded, setLoaded] = useState(false),
    [busy, setBusy] = useState(false);
  const refreshID = useRef(0),
    mounted = useRef(true);
  const refresh = useCallback(async () => {
    const id = ++refreshID.current;
    try {
      const [next, ts] = await Promise.all([
        api<Task[]>("/v1/tasks"),
        api<Template[]>("/v1/task-templates"),
      ]);
      if (mounted.current && id === refreshID.current) {
        setTasks(next);
        setTemplates(ts);
        setLoaded(true);
        setError("");
      }
    } catch (e) {
      if (mounted.current && id === refreshID.current) {
        setError((e as Error).message);
        setConnection("Disconnected");
      }
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    const stream = new EventSource("/v1/task-events");
    stream.addEventListener("invalidate", () => {
      setConnection("Live");
      void refresh();
    });
    stream.onopen = () => {
      setConnection("Live");
      void refresh();
    };
    stream.onerror = () => setConnection("Reconnecting");
    const focus = () => void refresh();
    window.addEventListener("focus", focus);
    const reconcile = window.setInterval(() => void refresh(), 30000);
    return () => {
      mounted.current = false;
      stream.close();
      window.clearInterval(reconcile);
      window.removeEventListener("focus", focus);
    };
  }, [refresh]);
  const selected = tasks.find((t) => t.id === selectedID);
  const select = (task: Task) => {
    setSelectedID(task.id);
    setPanel("details");
    setTab("overview");
    setInfo("");
    setActivationRequested(false);
    const url = new URL(window.location.href);
    url.searchParams.set("task", task.id);
    window.history.replaceState(null, "", url);
  };
  const open = (next: Panel) => {
    setPanel(next);
    setActivationRequested(false);
    setInfo("");
    setError("");
  };
  const mutate = async <T,>(
    path: string,
    method: string,
    body: unknown,
  ): Promise<T> => {
    setBusy(true);
    setError("");
    try {
      const result = await api<T>(path, method, body);
      await refresh();
      return result;
    } catch (e) {
      setError(
        e instanceof APIError && e.status === 409
          ? "This card changed. Refresh and retry your edit."
          : (e as Error).message,
      );
      if (e instanceof APIError && e.status === 409) void refresh();
      throw e;
    } finally {
      setBusy(false);
    }
  };
  const move = async (task: Task, column: Column) => {
    if (column === "polling") {
      const problem = pollingProblem(task.polling);
      if (problem) {
        setSelectedID(task.id);
        setPanel("details");
        setTab("polling");
        setActivationRequested(true);
        setInfo(problem);
        return;
      }
    }
    try {
      await mutate(`/v1/tasks/${task.id}/move`, "POST", {
        expected_version: task.version,
        column,
        position: Date.now(),
      });
      setInfo("");
    } catch (e) {
      if (column === "polling" && e instanceof APIError && e.status === 400) {
        setSelectedID(task.id);
        setPanel("details");
        setTab("polling");
        setActivationRequested(true);
        setInfo(e.message);
      }
    }
  };
  const drop = (e: React.DragEvent, column: Column) => {
    e.preventDefault();
    const task = tasks.find(
      (t) => t.id === e.dataTransfer.getData("text/task-id"),
    );
    if (task) void move(task, column);
  };
  const created = (task: Task) => {
    const url = new URL(window.location.href);
    url.searchParams.set("task", task.id);
    window.history.replaceState(null, "", url);
    setSelectedID(task.id);
    setPanel("details");
    setTab("overview");
    setInfo("Task created. Launch requests are started in Telegram.");
  };
  const sources = tasks.filter(isSource),
    cards = tasks.filter(
      (t) =>
        !isSource(t) &&
        `${t.title} ${t.description}`
          .toLowerCase()
          .includes(query.toLowerCase()),
    );
  return (
    <div className={`app ${collapsed ? "sources-collapsed" : ""}`}>
      <header className="app-header">
        <div className="brand">
          <span className="brand-mark">c</span>
          <div>
            <strong>Codex</strong>
            <span>Local task board</span>
          </div>
        </div>
        <div className="header-status">
          <span
            className={`connection-dot ${connection === "Live" ? "live" : ""}`}
          />
          {connection}
          <span className="local-label">Local workspace</span>
        </div>
        <button className="quiet" onClick={() => open("templates")}>
          Templates <span>{templates.length}</span>
        </button>
        <button onClick={() => open("add-chat")}>Add chat</button>
        <button className="primary" onClick={() => open("new-task")}>
          ＋ New task
        </button>
      </header>
      <aside className="sources">
        <div className="sources-heading">
          <h2>{collapsed ? "" : "Sources"}</h2>
          <button
            className="icon-button"
            aria-label={collapsed ? "Expand sources" : "Collapse sources"}
            onClick={() => setCollapsed(!collapsed)}
          >
            {collapsed ? "›" : "‹"}
          </button>
        </div>
        {collapsed ? (
          <button
            className="source-add icon-button"
            aria-label="Add source"
            onClick={() => open("new-source")}
          >
            ＋
          </button>
        ) : (
          <>
            <p className="sidebar-note">Create tasks as work arrives.</p>
            <button className="source-add" onClick={() => open("new-source")}>
              ＋ Add source
            </button>
            <div className="source-list">
              {sources.map((source) => (
                <button
                  key={source.id}
                  className={`source-item ${selectedID === source.id ? "selected" : ""}`}
                  onClick={() => select(source)}
                >
                  <span
                    className={`source-symbol ${source.column === "polling" ? "active" : ""}`}
                  >
                    {source.polling?.type === "cron" ? "◷" : "↗"}
                  </span>
                  <span>
                    <strong>{source.title}</strong>
                    <small>
                      {source.column === "polling"
                        ? "Polling"
                        : `Paused · ${labels[source.column]}`}
                    </small>
                  </span>
                  <i
                    className={`source-dot ${source.column === "polling" ? "active" : ""}`}
                  />
                </button>
              ))}
              {!sources.length && (
                <p className="empty-sources">
                  Connect a PR inbox or a schedule.
                </p>
              )}
            </div>
            <div className="sidebar-footer">
              <span className="tiny-label">HOW IT WORKS</span>
              <p>
                Sources copy a template into Inbox. Each new task gets its own
                launch request.
              </p>
              <button className="text-button" onClick={() => open("templates")}>
                Manage templates →
              </button>
            </div>
          </>
        )}
      </aside>
      <main className="board">
        <div className="board-heading">
          <div>
            <p className="eyebrow">YOUR WORKSPACE</p>
            <h1>
              Task board{" "}
              <span>
                {
                  tasks.filter((t) => !isSource(t) && t.column !== "done")
                    .length
                }
              </span>
            </h1>
          </div>
          <label className="search">
            <span aria-hidden="true">⌕</span>
            <input
              aria-label="Search tasks"
              placeholder="Find a task…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <kbd>⌕</kbd>
          </label>
        </div>
        {error && (
          <div role="alert" className="error-banner">
            <span>{error}</span>
            <button onClick={() => void refresh()}>Refresh</button>
          </div>
        )}
        <div className="board-columns">
          {columns.map((column) => {
            const list = cards
              .filter((t) => t.column === column)
              .sort(
                (a, b) => a.position - b.position || a.id.localeCompare(b.id),
              );
            return (
              <section
                key={column}
                className={`column ${column}`}
                aria-label={`${labels[column]} column`}
                onDragOver={(e) => {
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                }}
                onDrop={(e) => drop(e, column)}
              >
                <div className="column-heading">
                  <h2>
                    <span className="column-dot" />
                    {labels[column]}{" "}
                    <span className="count">{list.length}</span>
                  </h2>
                  <span className="column-hint">
                    {column === "inbox"
                      ? "Incoming"
                      : column === "todo"
                        ? "Next up"
                        : column === "polling"
                          ? "Waiting"
                          : "Finished"}
                  </span>
                </div>
                <div className="column-cards">
                  {list.map((task) => (
                    <TaskCard
                      key={task.id}
                      task={task}
                      selected={selectedID === task.id && panel === "details"}
                      onSelect={() => select(task)}
                    />
                  ))}
                  {!list.length && (
                    <div className="empty-column">
                      <span>
                        {column === "inbox"
                          ? "↓"
                          : column === "todo"
                            ? "＋"
                            : column === "polling"
                              ? "◷"
                              : "✓"}
                      </span>
                      <p>
                        {!loaded
                          ? "Loading…"
                          : column === "inbox"
                            ? "Nothing incoming"
                            : column === "todo"
                              ? "Room for your next task"
                              : column === "polling"
                                ? "No tasks waiting"
                                : "Your finished work"}
                      </p>
                      <small>
                        {column === "polling"
                          ? "Add conditions to start waiting."
                          : "Drag a task here or use Move."}
                      </small>
                    </div>
                  )}
                </div>
                {column === "todo" && (
                  <button
                    className="column-add"
                    onClick={() => open("new-task")}
                  >
                    ＋ Add a task
                  </button>
                )}
              </section>
            );
          })}
        </div>
        <footer className="board-footer">
          <span>Reading a card leaves its state unchanged.</span>
          <span>Launch approval happens in Telegram.</span>
        </footer>
      </main>
      <aside className="detail-panel" aria-label="Task details and settings">
        <div className="panel-header">
          <span className="eyebrow">
            {panel === "details"
              ? "TASK DETAILS"
              : panel === "templates"
                ? "TEMPLATES"
                : panel === "new-source"
                  ? "NEW SOURCE"
                  : panel === "add-chat"
                    ? "LINK A CHAT"
                    : "NEW TASK"}
          </span>
          {panel !== "details" && (
            <button
              className="icon-button"
              aria-label="Close editor"
              onClick={() => open("details")}
            >
              ×
            </button>
          )}
        </div>
        {panel === "details" && selected ? (
          <>
            <div className="detail-heading">
              <h2>{selected.title}</h2>
              <div className="detail-badges">
                <span className="pill">{labels[selected.column]}</span>
                {executionLabel(selected) && (
                  <span className="pill">{executionLabel(selected)}</span>
                )}
              </div>
            </div>
            <div className="move-control">
              <Field label="Move task">
                <select
                  aria-label="Move task"
                  value={selected.column}
                  disabled={busy}
                  onChange={(e) =>
                    void move(selected, e.target.value as Column)
                  }
                >
                  {columns.map((c) => (
                    <option key={c} value={c}>
                      {labels[c]}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            <div className="tabs" role="tablist">
              {(["overview", "settings", "polling", "activity"] as Tab[]).map(
                (t) => (
                  <button
                    key={t}
                    role="tab"
                    aria-selected={tab === t}
                    onClick={() => {
                      setTab(t);
                      setInfo("");
                      setActivationRequested(false);
                    }}
                  >
                    {t[0].toUpperCase() + t.slice(1)}
                  </button>
                ),
              )}
            </div>
            {info && (
              <p className="info-note" role="status">
                {info}
              </p>
            )}
            <div className="panel-body">
              {tab === "overview" ? (
                <Overview task={selected} templates={templates} />
              ) : tab === "settings" ? (
                <TaskSettings
                  key={selected.id}
                  task={selected}
                  busy={busy}
                  onSave={(input) =>
                    mutate<Task>(`/v1/tasks/${selected.id}`, "PATCH", input)
                  }
                />
              ) : tab === "polling" ? (
                <>
                  <PollingDiagnostics task={selected} />
                  <PollingForm
                    key={selected.id}
                    initial={selected.polling}
                    expectedVersion={selected.version}
                    templates={templates}
                    busy={busy}
                    activate={activationRequested}
                    onSave={async (p, activate, expectedVersion) => {
                      const task = await mutate<Task>(
                        `/v1/tasks/${selected.id}/polling`,
                        "PUT",
                        { expected_version: expectedVersion, polling: p },
                      );
                      let savedVersion = task.version;
                      if (activate) {
                        const moved = await mutate<Task>(
                          `/v1/tasks/${task.id}/move`,
                          "POST",
                          {
                            expected_version: task.version,
                            column: "polling",
                            position: task.position,
                          },
                        );
                        savedVersion = moved.version;
                      }
                      setInfo(
                        activate || task.column === "polling"
                          ? "Waiting is active."
                          : "Settings saved. Move to Polling to activate.",
                      );
                      setActivationRequested(false);
                      return savedVersion;
                    }}
                  />
                </>
              ) : (
                <Activity task={selected} />
              )}
            </div>
          </>
        ) : panel === "details" ? (
          <div className="welcome">
            <span className="welcome-icon">▤</span>
            <h2>
              A little space
              <br />
              for the next thing.
            </h2>
            <p>
              Select a card to see its chat, launch requests and waiting
              conditions.
            </p>
            <button onClick={() => open("new-task")}>
              Create your first task
            </button>
            <div className="welcome-note">
              <strong>One card, one cycle</strong>
              <p>
                A source creates tasks. A wait returns the same task to Inbox.
                Done stays done until you move it.
              </p>
            </div>
          </div>
        ) : panel === "new-task" ? (
          <NewTask
            templates={templates}
            busy={busy}
            onSave={async (input) =>
              created(await mutate<Task>("/v1/tasks", "POST", input))
            }
          />
        ) : panel === "new-source" ? (
          <NewSource
            templates={templates}
            busy={busy}
            onSave={async (input) =>
              created(await mutate<Task>("/v1/tasks", "POST", input))
            }
            onTemplates={() => open("templates")}
          />
        ) : panel === "add-chat" ? (
          <AddChat
            busy={busy}
            onSave={async (thread_id, title) =>
              created(
                await mutate<Task>("/v1/tasks/add-chat", "POST", {
                  thread_id,
                  title,
                }),
              )
            }
          />
        ) : (
          <TemplateManager
            templates={templates}
            busy={busy}
            onSave={async (template, input) => {
              return mutate<Template>(
                template
                  ? `/v1/task-templates/${template.id}`
                  : "/v1/task-templates",
                template ? "PUT" : "POST",
                input,
              );
            }}
            onDelete={async (template) => {
              await mutate(`/v1/task-templates/${template.id}`, "DELETE", {
                expected_version: template.version,
              });
            }}
          />
        )}
      </aside>
    </div>
  );
}
function ExternalLink({ url, label }: { url: string; label: string }) {
  const safe = safeExternalURL(url);
  return safe ? (
    <a className="detail-link" href={safe} target="_blank" rel="noreferrer">
      {label}
      <span>↗</span>
    </a>
  ) : (
    <span className="note">
      {label}: {url}
    </span>
  );
}
function Overview({ task, templates }: { task: Task; templates: Template[] }) {
  return (
    <div className="overview">
      {task.description && <p className="description">{task.description}</p>}
      {task.thread_id && (
        <a className="detail-link codex-link" href={codexURL(task.thread_id)}>
          Open in Codex <span>↗</span>
        </a>
      )}
      {task.source_url && (
        <ExternalLink url={task.source_url} label="Open source" />
      )}
      <PollingDiagnostics task={task} />
      {task.polling?.type === "pr_wait" && (
        <div className="summary-block">
          <h3>Waiting conditions · ANY</h3>
          <ul>
            {task.polling.config.conditions?.map((c) => (
              <li key={c}>{conditionLabels[c] || c}</li>
            ))}
          </ul>
        </div>
      )}
      {task.template_id && (
        <div className="summary-block">
          <h3>Template snapshot</h3>
          <p>
            {templates.find((t) => t.id === task.template_id)?.name ||
              "Template"}{" "}
            · v{task.template_version}
          </p>
          <small>
            Copied when this task was created. Later template edits affect
            future tasks.
          </small>
        </div>
      )}
      <dl className="metadata">
        <div>
          <dt>Created</dt>
          <dd>{formatTime(task.created_at)}</dd>
        </div>
        <div>
          <dt>Updated</dt>
          <dd>{formatTime(task.updated_at)}</dd>
        </div>
        {task.execution?.last_poll_at && (
          <div>
            <dt>Chat read</dt>
            <dd>{formatTime(task.execution.last_poll_at)}</dd>
          </div>
        )}
      </dl>
      <RequestHistory task={task} />
    </div>
  );
}
export function PollingDiagnostics({ task }: { task: Task }) {
  const p = task.polling;
  return (
    <section className="poll-diagnostics">
      <div className="diagnostic-heading">
        <span
          className={`connection-dot ${task.column === "polling" ? "live" : ""}`}
        />
        <strong>{pollingLabel(task)}</strong>
      </div>
      {p ? (
        <>
          <p>
            {p.type === "pr_review_source"
              ? "Assigned PRs"
              : p.type === "pr_changes_source"
                ? "My PRs requesting changes"
                : p.type === "cron"
                  ? "Cron schedule"
                  : "PR waiting"}{" "}
            · {p.mode}
          </p>
          <dl className="metadata">
            <div>
              <dt>Next check</dt>
              <dd>
                {task.column === "polling"
                  ? formatTime(p.next_check_at)
                  : "Paused"}
              </dd>
            </div>
            <div>
              <dt>Last attempt</dt>
              <dd>{formatTime(p.last_attempt_at)}</dd>
            </div>
            <div>
              <dt>Last success</dt>
              <dd>{formatTime(p.last_success_at)}</dd>
            </div>
          </dl>
          {p.last_error && <p className="error-note">{p.last_error}</p>}
        </>
      ) : (
        <p>No polling settings. Add them in the Polling tab.</p>
      )}
    </section>
  );
}
function RequestHistory({ task }: { task: Task }) {
  return (
    <section className="summary-block">
      <h3>
        Launch requests <span>{task.requests?.length || 0}</span>
      </h3>
      <p className="note">
        Start, dismiss and retry in the Telegram Control topic.
      </p>
      {task.requests?.map((r) => (
        <div className="request" key={r.id}>
          <div>
            <strong>{r.status.replaceAll("_", " ")}</strong>
            <small>{formatTime(r.created_at)}</small>
          </div>
          {r.target_thread_id && (
            <span className="note">Continues this same chat</span>
          )}
          {r.error_summary && <p className="error-note">{r.error_summary}</p>}
          <details>
            <summary>Frozen prompt</summary>
            <pre>{r.prompt}</pre>
          </details>
        </div>
      ))}
    </section>
  );
}
function TaskSettings({
  task,
  busy,
  onSave,
}: {
  task: Task;
  busy: boolean;
  onSave: (input: unknown) => Promise<Task>;
}) {
  const [title, setTitle] = useState(task.title),
    [description, setDescription] = useState(task.description),
    [sourceURL, setSourceURL] = useState(task.source_url),
    [config, setConfig] = useState(task.config),
    [version, setVersion] = useState(task.version),
    [message, setMessage] = useState("");
  const save = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const updated = await onSave({
        expected_version: version,
        title,
        description,
        source_url: sourceURL,
        config,
      });
      setVersion(updated.version);
      setMessage("Task settings saved.");
    } catch (e) {
      setMessage((e as Error).message);
    }
  };
  return (
    <form onSubmit={(e) => void save(e)}>
      <p className="note">
        Overrides apply to this task. Pending launch requests keep their frozen
        prompt and settings.
      </p>
      <Field label="Title">
        <input
          required
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
      </Field>
      <Field label="Description">
        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={3}
        />
      </Field>
      <Field label="Source URL">
        <input
          type="url"
          value={sourceURL}
          onChange={(e) => setSourceURL(e.target.value)}
        />
      </Field>
      <PromptFields value={config} onChange={setConfig} />
      {message && (
        <p role="status" className="info-note">
          {message}
        </p>
      )}
      <button className="primary" disabled={busy}>
        Save this task
      </button>
    </form>
  );
}
function Activity({ task }: { task: Task }) {
  const [events, setEvents] = useState<TaskEvent[]>([]),
    [error, setError] = useState("");
  useEffect(() => {
    let current = true;
    api<TaskEvent[]>(`/v1/tasks/${task.id}/events`)
      .then((e) => {
        if (current) setEvents(e);
      })
      .catch((e) => {
        if (current) setError(e.message);
      });
    return () => {
      current = false;
    };
  }, [task.id, task.updated_at]);
  return (
    <>
      {error && (
        <p role="alert" className="error-note">
          {error}
        </p>
      )}
      <h3>Task history</h3>
      <div className="timeline">
        {events.map((e) => (
          <div key={e.id}>
            <i />
            <strong>{e.kind.replaceAll("_", " ")}</strong>
            <small>{formatTime(e.created_at)}</small>
          </div>
        ))}
        {!events.length && <p className="note">No events yet.</p>}
      </div>
      <RequestHistory task={task} />
    </>
  );
}
function NewTask({
  templates,
  busy,
  onSave,
}: {
  templates: Template[];
  busy: boolean;
  onSave: (input: unknown) => Promise<void>;
}) {
  const [title, setTitle] = useState(""),
    [description, setDescription] = useState(""),
    [config, setConfig] = useState(emptyConfig()),
    [templateID, setTemplateID] = useState(""),
    [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await onSave(
        templateID
          ? { template_id: templateID, column: "todo", title }
          : { title, description, column: "todo", config },
      );
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const template = templates.find((t) => t.id === templateID);
  return (
    <div className="panel-body">
      <h2>Something to work on</h2>
      <p className="note">New manual tasks start in TODO.</p>
      <form onSubmit={(e) => void submit(e)}>
        <Field label="Template">
          <select
            value={templateID}
            onChange={(e) => setTemplateID(e.target.value)}
          >
            <option value="">Start blank</option>
            {templates.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Title">
          <input
            required={!templateID}
            value={title}
            placeholder={template?.definition.title || "Give your task a name"}
            onChange={(e) => setTitle(e.target.value)}
          />
        </Field>
        {template ? (
          <div className="template-preview">
            <strong>
              {template.name} · v{template.version}
            </strong>
            <p>
              {template.definition.description ||
                "Prompts and execution settings will be copied."}
            </p>
            <p className="note">
              After creation, use the task Settings tab for overrides.
            </p>
          </div>
        ) : (
          <>
            <Field label="Description">
              <textarea
                rows={3}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </Field>
            <PromptFields value={config} onChange={setConfig} />
          </>
        )}
        {error && (
          <p role="alert" className="error-note">
            {error}
          </p>
        )}
        <button className="primary" disabled={busy}>
          Create task
        </button>
      </form>
    </div>
  );
}
function NewSource({
  templates,
  busy,
  onSave,
  onTemplates,
}: {
  templates: Template[];
  busy: boolean;
  onSave: (input: unknown) => Promise<void>;
  onTemplates: () => void;
}) {
  const [title, setTitle] = useState(""),
    [polling, setPolling] = useState(defaultPolling("pr_review_source")),
    [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const problem = pollingProblem(polling);
    if (problem) {
      setError(problem);
      return;
    }
    try {
      await onSave({ title, column: "todo", config: emptyConfig(), polling });
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <div className="panel-body">
      <h2>Connect a source</h2>
      <p className="note">
        Sources are tasks that create other tasks. Start paused, then move to
        Polling when ready.
      </p>
      {!templates.length && (
        <div className="info-note">
          Create a task template first.
          <button className="text-button" onClick={onTemplates}>
            Manage templates →
          </button>
        </div>
      )}
      <form onSubmit={(e) => void submit(e)}>
        <Field label="Source name">
          <input
            required
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="My assigned PRs"
          />
        </Field>
        <PollingFields
          value={polling}
          onChange={setPolling}
          templates={templates}
          sourceOnly
        />
        {error && (
          <p role="alert" className="error-note">
            {error}
          </p>
        )}
        <button className="primary" disabled={busy}>
          Create paused source
        </button>
      </form>
    </div>
  );
}
function AddChat({
  busy,
  onSave,
}: {
  busy: boolean;
  onSave: (thread: string, title: string) => Promise<void>;
}) {
  const [thread, setThread] = useState(""),
    [title, setTitle] = useState(""),
    [list, setList] = useState<{ id: string; title?: string; name?: string }[]>(
      [],
    ),
    [cursor, setCursor] = useState(""),
    [error, setError] = useState("");
  const load = useCallback(async (next = "") => {
    try {
      const payload = await api<{
        threads?: { id: string; title?: string; name?: string }[];
        data?: { id: string; title?: string; name?: string }[];
        nextCursor?: string;
        next_cursor?: string;
      }>(`/v1/threads?limit=100&cursor=${encodeURIComponent(next)}`);
      setList((old) =>
        next
          ? [...old, ...(payload.threads || payload.data || [])]
          : payload.threads || payload.data || [],
      );
      setCursor(payload.next_cursor || payload.nextCursor || "");
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const id = parseThreadID(thread);
      if (!id) throw new Error("Enter a thread ID or a codex://threads link.");
      await onSave(id, title);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <div className="panel-body">
      <h2>Bring a chat here</h2>
      <p className="note">
        Links an existing Codex chat in TODO. Adding the same chat again opens
        its existing card.
      </p>
      <form onSubmit={(e) => void submit(e)}>
        <Field label="Recent chats">
          <select
            value={list.some((t) => t.id === thread) ? thread : ""}
            onChange={(e) => {
              const t = list.find((t) => t.id === e.target.value);
              setThread(e.target.value);
              setTitle(t?.title || t?.name || "");
            }}
          >
            <option value="">Choose a chat</option>
            {list.map((t) => (
              <option key={t.id} value={t.id}>
                {t.title || t.name || t.id}
              </option>
            ))}
          </select>
        </Field>
        {cursor && (
          <button type="button" onClick={() => void load(cursor)}>
            Load more chats
          </button>
        )}
        <Field
          label="Thread ID"
          hint="Paste a thread ID or a codex://threads link."
        >
          <input
            required
            value={thread}
            onChange={(e) => setThread(e.target.value)}
          />
        </Field>
        <Field label="Card title">
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Use chat title"
          />
        </Field>
        {error && (
          <p role="alert" className="error-note">
            {error}
          </p>
        )}
        <button className="primary" disabled={busy}>
          Add chat to TODO
        </button>
      </form>
    </div>
  );
}
function TemplateManager({
  templates,
  busy,
  onSave,
  onDelete,
}: {
  templates: Template[];
  busy: boolean;
  onSave: (t: Template | undefined, input: unknown) => Promise<Template>;
  onDelete: (t: Template) => Promise<void>;
}) {
  const [selected, setSelected] = useState("new"),
    [message, setMessage] = useState("");
  return (
    <div className="panel-body">
      <h2>Task templates</h2>
      <p className="note">
        Template changes affect future tasks. Existing tasks and launch requests
        keep their copied settings.
      </p>
      <Field label="Template">
        <select
          value={selected}
          onChange={(e) => {
            setSelected(e.target.value);
            setMessage("");
          }}
        >
          <option value="new">＋ Create a template</option>
          {templates.map((t) => (
            <option value={t.id} key={t.id}>
              {t.name} · v{t.version}
            </option>
          ))}
        </select>
      </Field>
      <TemplateEditor
        key={selected}
        template={templates.find((t) => t.id === selected)}
        templates={templates}
        busy={busy}
        onSave={async (t, input) => {
          try {
            const saved = await onSave(t, input);
            setSelected(saved.id);
            setMessage("Template saved for future tasks.");
            return saved;
          } catch (e) {
            setMessage((e as Error).message);
            throw e;
          }
        }}
        onDelete={async (t) => {
          try {
            await onDelete(t);
            setSelected("new");
            setMessage("Template deleted.");
          } catch (e) {
            setMessage((e as Error).message);
          }
        }}
      />
      {message && (
        <p className="info-note" role="status">
          {message}
        </p>
      )}
    </div>
  );
}
export function TemplateEditor({
  template,
  templates,
  busy,
  onSave,
  onDelete,
}: {
  template?: Template;
  templates: Template[];
  busy: boolean;
  onSave: (t: Template | undefined, input: unknown) => Promise<Template>;
  onDelete: (t: Template) => Promise<void>;
}) {
  const [version, setVersion] = useState(template?.version || 0);
  const [name, setName] = useState(template?.name || ""),
    [definition, setDefinition] = useState<Definition>(
      template?.definition || blankDefinition(),
    ),
    [child, setChild] = useState(!!template?.definition.polling),
    [polling, setPolling] = useState<Polling>(
      template?.definition.polling || {
        ...defaultPolling(),
        config: {
          login: "",
          pr_id: "{{pr_id}}",
          author: "{{author}}",
          conditions: ["new_commit", "all_answered"],
          interval_seconds: 60,
        },
      },
    ),
    [error, setError] = useState(""),
    [confirmDelete, setConfirmDelete] = useState(false);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (child) {
      const problem = pollingProblem(polling, true);
      if (problem) {
        setError(problem);
        return;
      }
    }
    setError("");
    try {
      const saved = await onSave(template, {
        expected_version: version,
        name,
        definition: {
          ...definition,
          ...(child ? { polling } : { polling: undefined }),
        },
      });
      setVersion(saved.version);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <form onSubmit={(e) => void save(e)}>
      <Field label="Template name">
        <input
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="PR review"
        />
      </Field>
      <Field
        label="Task title"
        hint="PR variables: {{pr_title}}, {{pr_id}}, {{pr_url}}, {{author}}, {{login}}. Cron: {{date}}, {{scheduled_at}}."
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
      <PromptFields
        value={definition.config}
        onChange={(config) => setDefinition({ ...definition, config })}
      />
      <label className="check-line">
        <input
          type="checkbox"
          checked={child}
          onChange={(e) => setChild(e.target.checked)}
        />
        Default child waiting settings
      </label>
      {child && (
        <div className="child-settings">
          <PollingFields
            value={polling}
            onChange={setPolling}
            templates={templates}
            child
          />
        </div>
      )}
      {error && (
        <p role="alert" className="error-note">
          {error}
        </p>
      )}
      <div className="form-actions">
        <button className="primary" disabled={busy}>
          {template ? "Save template" : "Create template"}
        </button>
        {template && (
          <button
            type="button"
            className="danger-button"
            onClick={() => setConfirmDelete(!confirmDelete)}
          >
            Delete
          </button>
        )}
      </div>
      {template && confirmDelete && (
        <div className="error-note">
          Delete this template? Tasks already created will stay.
          <button
            className="danger-button"
            type="button"
            disabled={busy}
            onClick={() => void onDelete({ ...template, version })}
          >
            Confirm delete
          </button>
        </div>
      )}
    </form>
  );
}
