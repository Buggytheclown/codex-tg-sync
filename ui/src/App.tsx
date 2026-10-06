import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "./api";
import type { Column, Polling, Task, TaskEvent } from "./model";
import {
  codexURL,
  columns,
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
import { Field, PollingForm, PromptFields, conditionLabels } from "./forms";

type Panel = "details" | "new-task" | "new-source" | "add-chat";
import { SourceEditor } from "./SourceEditor";
import { PollingDiagnostics } from "./PollingDiagnostics";
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
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selectedID, setSelectedID] = useState(
      () => new URLSearchParams(window.location.search).get("task") || "",
    ),
    [panel, setPanel] = useState<Panel>("details"),
    [tab, setTab] = useState<Tab>("overview");
  const [collapsed, setCollapsed] = useState(false),
    [query, setQuery] = useState(""),
    [error, setError] = useState(""),
    [info, setInfo] = useState("");
  const [showDone, setShowDone] = useState(() => {
    try {
      return window.localStorage.getItem("codex-tg.show-done") === "true";
    } catch {
      return false;
    }
  });
  const [activationRequested, setActivationRequested] = useState(false);
  const [connection, setConnection] = useState("Connecting"),
    [loaded, setLoaded] = useState(false),
    [busy, setBusy] = useState(false);
  const refreshID = useRef(0),
    mounted = useRef(true);
  const refresh = useCallback(async () => {
    const id = ++refreshID.current;
    try {
      const next = await api<Task[]>("/v1/tasks");
      if (mounted.current && id === refreshID.current) {
        setTasks(next);
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
    setInfo(
      isSource(task)
        ? "Source created."
        : "Task created. Launch requests are started in Telegram.",
    );
  };
  const visibleColumns = columns.filter((column) => showDone || column !== "done");
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
                Sources create tasks in Inbox. Each new task gets its own launch
                request.
              </p>
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
          <div className="board-tools">
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
            <button
              aria-pressed={showDone}
              onClick={() => {
                const visible = !showDone;
                setShowDone(visible);
                try {
                  window.localStorage.setItem("codex-tg.show-done", String(visible));
                } catch {
                  // The toggle still works when browser storage is unavailable.
                }
              }}
            >
              {showDone ? "Hide Done" : "Show Done"}
            </button>
          </div>
        </div>
        {error && (
          <div role="alert" className="error-banner">
            <span>{error}</span>
            <button onClick={() => void refresh()}>Refresh</button>
          </div>
        )}
        <div className={`board-columns${showDone ? "" : " done-hidden"}`}>
          {visibleColumns.map((column) => {
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
              ? selected && isSource(selected)
                ? "SOURCE SETTINGS"
                : "TASK DETAILS"
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
        {panel === "details" && selected && isSource(selected) ? (
          <>
            <div className="detail-heading">
              <h2>{selected.title}</h2>
            </div>
            <div className="panel-body">
              <SourceEditor
                key={selected.id}
                task={selected}
                busy={busy}
                onSave={async (p, _enabled, expectedVersion) => {
                  const saved = await mutate<Task>(
                    `/v1/tasks/${selected.id}/polling`,
                    "PUT",
                    { expected_version: expectedVersion, polling: p },
                  );
                  return saved.version;
                }}
                onToggle={async (enabled, expectedVersion) => {
                  const moved = await mutate<Task>(
                    `/v1/tasks/${selected.id}/move`,
                    "POST",
                    {
                      expected_version: expectedVersion,
                      column: enabled ? "polling" : "todo",
                      position: selected.position,
                    },
                  );
                  return moved.version;
                }}
              />
            </div>
          </>
        ) : panel === "details" && selected ? (
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
                <Overview task={selected} />
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
            busy={busy}
            onSave={async (input) =>
              created(await mutate<Task>("/v1/tasks", "POST", input))
            }
          />
        ) : panel === "new-source" ? (
          <NewSource
            busy={busy}
            onSave={async (input) =>
              created(await mutate<Task>("/v1/tasks", "POST", input))
            }
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
        ) : null}
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
function Overview({ task }: { task: Task }) {
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
      {task.source_task_id && (
        <a
          className="detail-link"
          href={`?task=${encodeURIComponent(task.source_task_id)}`}
        >
          Created by source
        </a>
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
        Changes apply to this task. Pending launch requests keep their frozen
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
  busy,
  onSave,
}: {
  busy: boolean;
  onSave: (input: unknown) => Promise<void>;
}) {
  const [title, setTitle] = useState(""),
    [description, setDescription] = useState(""),
    [config, setConfig] = useState(emptyConfig()),
    [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await onSave({ title, description, column: "todo", config });
    } catch (err) {
      setError((err as Error).message);
    }
  };
  return (
    <div className="panel-body">
      <h2>Something to work on</h2>
      <p className="note">New manual tasks start in TODO.</p>
      <form onSubmit={(e) => void submit(e)}>
        <Field label="Title">
          <input
            required
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Give your task a name"
          />
        </Field>
        <Field label="Description">
          <textarea
            rows={3}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>
        <PromptFields value={config} onChange={setConfig} />
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
  busy,
  onSave,
}: {
  busy: boolean;
  onSave: (input: unknown) => Promise<void>;
}) {
  return (
    <div className="panel-body">
      <h2>Connect a source</h2>
      <SourceEditor
        busy={busy}
        onSave={async (polling, enabled, _version, title) => {
          await onSave({
            title,
            column: enabled ? "polling" : "todo",
            config: emptyConfig(),
            polling,
          });
          return 1;
        }}
      />
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
