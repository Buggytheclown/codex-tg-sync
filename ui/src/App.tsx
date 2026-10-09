import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "./api";
import { compareTasks, placeTask } from "./taskOrder";
import type { Column, Polling, Priority, Task, TaskCard as Card, TaskEvent } from "./model";
import {
  compactTask,
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
import { Field, PromptFields, TaskChatForm } from "./forms";

type Panel = "details" | "new-task" | "new-source" | "add-chat";
import { SourceEditor } from "./SourceEditor";
import { WorkflowForm } from "./Workflow";
import { TicketControl } from "./TicketControl";
type Tab = "overview" | "polling" | "history";
export function TaskCard({
  task,
  selected,
  onSelect,
  onPriority,
  onTicket,
  onLink,
  controlsDisabled = false,
  grabbed = false,
  saving = false,
  disabled = false,
  onDragStart,
  onDragEnd,
}: {
  task: Card;
  selected: boolean;
  onSelect: () => void;
  onPriority?: (priority: Priority) => Promise<void>;
  onTicket?: (url: string, version: number) => Promise<Card>;
  onLink?: (threadID: string, version: number) => Promise<Task>;
  controlsDisabled?: boolean;
  grabbed?: boolean;
  saving?: boolean;
  disabled?: boolean;
  onDragStart?: (e: React.DragEvent<HTMLButtonElement>) => void;
  onDragEnd?: () => void;
}) {
  const execution = executionLabel(task);
  const polling = pollingLabel(task);
  const pollingTone = polling === "Waiting" ? "active" : polling === "No polling" ? "" : "warning";
  const [linking, setLinking] = useState(false);
  const [controlError, setControlError] = useState("");
  return (
    <div className={`card-with-link${selected ? " selected" : ""}${grabbed ? " grabbed" : ""}${saving ? " saving" : ""}`}>
    <button
      className={`task-card ${selected ? "selected" : ""}${grabbed ? " grabbed" : ""}${saving ? " saving" : ""}`}
      data-task-id={task.id}
      aria-busy={saving}
      onClick={onSelect}
      draggable={!disabled}
      onDragEnd={onDragEnd}
      onDragStart={(e) => {
        e.dataTransfer.setData("text/task-id", task.id);
        e.dataTransfer.effectAllowed = "move";
        onDragStart?.(e);
      }}
      aria-pressed={selected}
    >
      {grabbed && <span className="drag-badge">Moving</span>}
      {saving && (
        <span className="save-badge" role="status">
          Saving position…
        </span>
      )}
      <span className="card-top">
        <span className="card-grip" aria-hidden="true">⠿</span>
        <span className="eyebrow">
          {task.thread_id ? "CHAT" : task.source_key ? "AUTOMATION" : "TASK"}
        </span>
      </span>
      <span className="card-title">
        <strong>{task.title}</strong>
      </span>
      {task.description && (
        <span className="card-description">{task.description}</span>
      )}
      <span className="card-meta">
        <span
          className={`poll-chip ${pollingTone}`}
        >
          {polling}
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
      <select className={`card-priority ${task.priority || "normal"}`} aria-label={`Priority for ${task.title}`}
        title={`${task.priority || "normal"} priority`} value={task.priority || "normal"} disabled={controlsDisabled || !onPriority}
        onChange={(e) => {
          setControlError("");
          void onPriority?.(e.target.value as Priority).catch(err => setControlError(err.message));
        }}>
        <option value="low">↓ Low</option><option value="normal">— Normal</option><option value="high">↑ High</option>
      </select>
    <div className="card-controls">
    {onTicket && <TicketControl task={task} busy={controlsDisabled} onSave={onTicket} />}
      {task.thread_id
        ? <a className="card-codex-link card-footer-link" href={codexURL(task.thread_id)} draggable={false}>Open in Codex ↗</a>
        : <button className="card-codex-link card-footer-link" type="button" aria-label="Link Codex: no chat linked" aria-expanded={linking} disabled={controlsDisabled || !onLink}
          onClick={() => setLinking(!linking)}>Link Codex ＋</button>}
    </div>
    {linking && !task.thread_id && onLink && <div className="card-chat-form">
      <TaskChatForm task={task} busy={controlsDisabled} onLink={onLink} inline onCancel={() => setLinking(false)} />
    </div>}
    {controlError && <p role="alert" className="error-note">{controlError}</p>}
    </div>
  );
}
export function App() {
  const [tasks, setTasks] = useState<Card[]>([]);
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
  const [drag, setDrag] = useState<{ id: string; height: number } | null>(null);
  const [insertion, setInsertion] = useState<{ column: Column; beforeID: string } | null>(null);
  const [savingID, setSavingID] = useState("");
  const dragRef = useRef<string | null>(null),
    pendingMove = useRef(false),
    queuedRefresh = useRef(false),
    refreshError = useRef(false),
    cancelledDrag = useRef(false);
  const refreshID = useRef(0),
    mounted = useRef(true);
  const refresh = useCallback(async () => {
    if (dragRef.current || pendingMove.current) {
      queuedRefresh.current = true;
      return;
    }
    queuedRefresh.current = false;
    const id = ++refreshID.current;
    try {
      const next = await api<Card[]>("/v1/task-cards");
      if (mounted.current && id === refreshID.current) {
        if (dragRef.current || pendingMove.current) {
          queuedRefresh.current = true;
          return;
        }
        setTasks(next);
        setLoaded(true);
        if (refreshError.current) setError("");
        refreshError.current = false;
      }
    } catch (e) {
      if (mounted.current && id === refreshID.current) {
        refreshError.current = true;
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
      setDetailRevision((n) => n + 1);
    });
    stream.onopen = () => {
      setConnection("Live");
      void refresh();
      setDetailRevision((n) => n + 1);
    };
    stream.onerror = () => setConnection("Reconnecting");
    const focus = () => { void refresh(); setDetailRevision((n) => n + 1); };
    window.addEventListener("focus", focus);
    const reconcile = window.setInterval(() => { void refresh(); setDetailRevision((n) => n + 1); }, 30000);
    return () => {
      mounted.current = false;
      stream.close();
      window.clearInterval(reconcile);
      window.removeEventListener("focus", focus);
    };
  }, [refresh]);
  const clearDrag = (reconcile = true) => {
    dragRef.current = null;
    setDrag(null);
    setInsertion(null);
    if (reconcile && queuedRefresh.current) void refresh();
  };
  useEffect(() => {
    const escape = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || !dragRef.current) return;
      cancelledDrag.current = true;
      dragRef.current = null;
      setDrag(null);
      setInsertion(null);
      if (queuedRefresh.current) void refresh();
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [refresh]);
  const selectedCard = tasks.find((t) => t.id === selectedID);
  const [detail, setDetail] = useState<Task>();
  const [detailError, setDetailError] = useState("");
  const [detailRevision, setDetailRevision] = useState(0);
  const detailID = useRef(0);
  const latestSelectedCard = useRef(selectedCard);
  latestSelectedCard.current = selectedCard;
  const latestDetail = useRef(detail);
  latestDetail.current = detail;
  const detailVersion = useRef(0);
  const consumedDetailRevision = useRef(-1);
  const detailSelection = useRef(selectedID);
  if (detailSelection.current !== selectedID) {
    detailSelection.current = selectedID;
    detailVersion.current = 0;
  }
  const selected = detail?.id === selectedID && selectedCard ? detail : undefined;
  useEffect(() => {
    const request = ++detailID.current;
    if (panel !== "details" || !selectedCard) return;
    if (detail?.id === selectedCard.id && detailVersion.current === selectedCard.version && consumedDetailRevision.current === detailRevision) return;
    consumedDetailRevision.current = detailRevision;
    setDetailError("");
    api<Task>(`/v1/tasks/${selectedCard.id}`).then((task) => {
      if (mounted.current && request === detailID.current && task.version >= Math.max(detailVersion.current, latestSelectedCard.current?.version || 0)) { detailVersion.current = task.version; setDetail(task); }
    }).catch((e) => { if (mounted.current && request === detailID.current) setDetailError(e.message); });
    return () => { ++detailID.current; };
  }, [selectedCard?.id, selectedCard?.version, panel, detailRevision]);
  const select = (task: Card) => {
    detailVersion.current = 0;
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
      if (result && typeof result === "object" && "config" in result) {
        const saved = result as unknown as Task;
        setDetail((old) => old?.id === saved.id && old.version <= saved.version ? saved : old);
        if (detailSelection.current === saved.id) detailVersion.current = saved.version;
      }
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
  const saveCardPriority = async (task: Card, priority: Priority) => {
    if (busy || pendingMove.current || dragRef.current) return;
    setBusy(true);
    setError("");
    try {
      const saved = await api<Card>(`/v1/tasks/${task.id}/priority`, "POST", {
        expected_version: task.version, priority,
      });
      if (!mounted.current) return;
      // A compact reply may advance details only for the exact edit we own.
      // It cannot certify settings we have never loaded at this version.
      const knownSettings = latestDetail.current?.id === saved.id &&
        latestDetail.current.version === task.version &&
        saved.version === task.version + 1 && saved.priority === priority;
      if (detailSelection.current === saved.id && knownSettings) detailVersion.current = saved.version;
      setDetail(old => {
        if (old?.id !== saved.id || old.version !== task.version ||
          saved.version !== task.version + 1 || saved.priority !== priority) return old;
        return { ...old, priority: saved.priority, version: saved.version, updated_at: saved.updated_at };
      });
      setTasks(current => current.map(card => card.id === saved.id && card.version <= saved.version ? saved : card));
      await refresh();
    } catch (err) {
      if (err instanceof APIError && err.status === 409) void refresh();
      throw err;
    } finally { if (mounted.current) setBusy(false); }
  };
  const saveTicket = async (task: Card | Task, ticketURL: string, expectedVersion: number): Promise<Card> => {
    if (busy || pendingMove.current || dragRef.current) throw new Error("Another edit is being saved. Try again.");
    setBusy(true);
    setError("");
    try {
      const saved = await api<Card>(`/v1/tasks/${task.id}/ticket`, "POST", {
        expected_version: expectedVersion, ticket_url: ticketURL,
      });
      if (!mounted.current) return saved;
      const knownSettings = latestDetail.current?.id === saved.id &&
        latestDetail.current.version === expectedVersion &&
        saved.version === expectedVersion + 1 && (saved.ticket_url || "") === ticketURL;
      if (detailSelection.current === saved.id && knownSettings) detailVersion.current = saved.version;
      setDetail(old => {
        if (old?.id !== saved.id || old.version !== expectedVersion ||
          saved.version !== expectedVersion + 1 || (saved.ticket_url || "") !== ticketURL) return old;
        return { ...old, ticket_url: saved.ticket_url, version: saved.version, updated_at: saved.updated_at };
      });
      setTasks(current => current.map(card => card.id === saved.id && card.version <= saved.version ? saved : card));
      await refresh();
      return saved;
    } catch (err) {
      if (err instanceof APIError && err.status === 409) void refresh();
      throw err;
    } finally { if (mounted.current) setBusy(false); }
  };
  const move = async (task: Card, column: Column, beforeID = "") => {
    if (pendingMove.current || busy) return;
    if (column === "polling" && task.column !== column) {
      const problem = task.polling ? task.polling.problem : pollingProblem();
      if (problem) {
        setSelectedID(task.id);
        setPanel("details");
        setTab("polling");
        setActivationRequested(true);
        setInfo(problem);
        if (queuedRefresh.current) void refresh();
        return;
      }
    }
    const previous = tasks;
    pendingMove.current = true;
    ++refreshID.current; // Retire snapshots started before the optimistic move.
    setSavingID(task.id);
    setBusy(true);
    setError("");
    refreshError.current = false;
    setTasks(placeTask(tasks, task, column, beforeID));
    try {
      const sameColumn = task.column === column;
      const response = await api<Task | Card>(`/v1/tasks/${task.id}/${sameColumn ? "reorder" : "move"}`, "POST", {
        expected_version: task.version,
        column,
        before_task_id: beforeID,
      });
      if (!mounted.current) return;
      const saved = "config" in response ? compactTask(response) : response;
      // Compact ordering replies can advance the editable version only when
      // the loaded settings are exactly the version this command reordered.
      const knownSettings = latestDetail.current?.id === saved.id &&
        latestDetail.current.version === task.version && saved.version === task.version + 1;
      if (detailSelection.current === saved.id && ("config" in response || knownSettings)) {
        detailVersion.current = saved.version;
      }
      setDetail((old) => {
        if (old?.id !== saved.id || old.version > saved.version) return old;
        if ("config" in response) return response;
        if (old.version !== task.version || saved.version !== task.version + 1) return old;
        return { ...old, position: saved.position, column: saved.column, version: saved.version, updated_at: saved.updated_at };
      });
      setTasks((current) =>
        current.map((t) => {
          if (t.id !== saved.id) return t;
          // Server insertion may include cards created during this gesture.
          // Keep all visible ordering coordinates together until a complete
          // snapshot replaces them; merge the returned version/settings now.
          return saved.column === t.column
            ? { ...saved, position: t.position }
            : saved;
        }),
      );
      setInfo("");
    } catch (e) {
      if (!mounted.current) return;
      setTasks(previous);
      setError(e instanceof APIError && e.status === 409
        ? "This card or its position changed. The board is refreshing; retry your move."
        : `Position was not saved: ${(e as Error).message}`);
      if (column === "polling" && e instanceof APIError && e.status === 400) {
        setSelectedID(task.id);
        setPanel("details");
        setTab("polling");
        setActivationRequested(true);
        setInfo(e.message);
      }
    } finally {
      pendingMove.current = false;
      ++refreshID.current;
      if (mounted.current) {
        setSavingID("");
        setBusy(false);
        // Save settles from its response; reconciliation runs independently.
        void refresh();
      }
    }
  };
  const insertionAt = (element: HTMLElement, y: number, column: Column) => {
    const candidates = Array.from(element.querySelectorAll<HTMLElement>("[data-task-id]"))
      .filter((card) => card.dataset.taskId !== dragRef.current);
    const before = candidates.find((card) => {
      const rect = (card.closest(".card-with-link") || card).getBoundingClientRect();
      return y < rect.top + rect.height / 2;
    });
    return { column, beforeID: before?.dataset.taskId || "" };
  };
  const drop = (e: React.DragEvent<HTMLElement>, column: Column) => {
    e.preventDefault();
    if (cancelledDrag.current || pendingMove.current) return;
    const task = tasks.find((t) => t.id === (dragRef.current || e.dataTransfer.getData("text/task-id")));
    // The visible placeholder is the release target. With no prior dragover,
    // derive it from the release point (also supports dropping into empty columns).
    const target = insertion?.column === column ? insertion : insertionAt(e.currentTarget, e.clientY, column);
    clearDrag(false);
    if (task) void move(task, column, target.beforeID);
    else if (queuedRefresh.current) void refresh();
  };
  const created = (task: Task) => {
    const url = new URL(window.location.href);
    url.searchParams.set("task", task.id);
    window.history.replaceState(null, "", url);
    detailVersion.current = 0;
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
              .sort(compareTasks);
            const placeholder = drag && insertion?.column === column ? (
              <div
                className="drop-placeholder"
                role="status"
                aria-label="Drop task here"
                style={{ minHeight: drag.height || 100 }}
              >
                Drop here
              </div>
            ) : null;
            return (
              <section
                key={column}
                className={`column ${column}`}
                aria-label={`${labels[column]} column`}
                onDragOver={(e) => {
                  if (!dragRef.current || pendingMove.current) return;
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                  setInsertion(insertionAt(e.currentTarget, e.clientY, column));
                }}
                onDragLeave={(e) => {
                  if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setInsertion(null);
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
                    <div className="card-slot" key={task.id}>
                      {insertion?.beforeID === task.id && placeholder}
                      <TaskCard
                        task={task}
                        selected={selectedID === task.id && panel === "details"}
                        grabbed={drag?.id === task.id}
                        saving={savingID === task.id}
                        disabled={busy}
                        onSelect={() => select(task)}
                        controlsDisabled={busy || !!drag}
                        onPriority={(priority) => saveCardPriority(task, priority)}
                        onTicket={(url, version) => saveTicket(task, url, version)}
                        onLink={(thread_id, expected_version) => mutate<Task>(`/v1/tasks/${task.id}/chat`, "POST", { expected_version, thread_id })}
                        onDragStart={(e) => {
                          if (busy) {
                            e.preventDefault();
                            return;
                          }
                          cancelledDrag.current = false;
                          dragRef.current = task.id;
                          queuedRefresh.current = true;
                          ++refreshID.current;
                          setDrag({
                            id: task.id,
                            height: (e.currentTarget.closest(".card-with-link") || e.currentTarget).getBoundingClientRect().height,
                          });
                        }}
                        onDragEnd={() => clearDrag()}
                      />
                    </div>
                  ))}
                  {insertion?.beforeID === "" && placeholder}
                  {!list.length && !placeholder && (
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
      <aside className="detail-panel" aria-label="Task details and settings" onKeyDown={(e) => {
        if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || e.repeat || e.nativeEvent.isComposing) return;
        const form = (e.target as HTMLElement).closest("form");
        if (!form || busy) return;
        const submitter = form.querySelector<HTMLButtonElement>('button:not([type="button"]):not([type="reset"])');
        if (!submitter || submitter.disabled) return;
        e.preventDefault();
        form.requestSubmit(submitter);
      }}>
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
        {panel === "details" && selected && detailError && (
          <div className="panel-body">
            <p role="alert" className="error-note">{detailError}</p>
            <button onClick={() => setDetailRevision((n) => n + 1)}>Retry details</button>
          </div>
        )}
        {panel === "details" && selectedCard && !selected ? (
          <div className="panel-body"><h2>{selectedCard.title}</h2>
            {detailError ? <><p role="alert" className="error-note">{detailError}</p><button onClick={() => setDetailRevision((n) => n + 1)}>Retry details</button></> : <p role="status">Loading details…</p>}
          </div>
        ) : panel === "details" && selected && isSource(selected) ? (
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
                  return saved;
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
                  return moved;
                }}
              />
            </div>
          </>
        ) : panel === "details" && selected ? (
          <>
            <div className="detail-heading">
              {tab !== "overview" && <h2>{selected.title}</h2>}
              <div className="detail-badges">
                <span className="pill">{labels[selected.column]}</span>
                {executionLabel(selected) && (
                  <span className={`pill execution ${executionLabel(selected).toLowerCase()}`}>{executionLabel(selected)}</span>
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
                    void move(compactTask(selected), e.target.value as Column)
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
              {(["overview", "polling", "history"] as Tab[]).map(
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
                <Overview key={selected.id} task={selected} busy={busy}
                  onSave={(input) => mutate<Task>(`/v1/tasks/${selected.id}`, "PATCH", input)}
                  onLink={(thread_id, expected_version) => mutate<Task>(`/v1/tasks/${selected.id}/chat`, "POST", { expected_version, thread_id })}
                  onTicket={(url, version) => saveTicket(selected, url, version)} />
              ) : tab === "polling" ? (
                <WorkflowForm key={`workflow-${selected.id}`} task={selected} busy={busy} configure={activationRequested}
                  onSave={input => mutate<Task>(`/v1/tasks/${selected.id}`, "PATCH", input)}
                  onActivate={async task => {
                    const moved = await mutate<Task>(`/v1/tasks/${task.id}/move`, "POST", {
                      expected_version: task.version, column: "polling", position: task.position,
                    });
                    setActivationRequested(false);
                    return moved;
                  }}
                  onLink={(thread_id, expected_version) => mutate<Task>(`/v1/tasks/${selected.id}/chat`, "POST", { expected_version, thread_id })} />
              ) : (
                <History task={selected} />
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
            onSave={async (input) => {
              const task = await mutate<Task>("/v1/tasks", "POST", input);
              created(task);
              return task;
            }}
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
function Overview({ task, busy, onSave, onLink, onTicket }: { task: Task; busy: boolean; onSave: (input: unknown) => Promise<Task>; onLink: (threadID: string, version: number) => Promise<Task>; onTicket: (url: string, version: number) => Promise<Card> }) {
  return (
    <div className="overview">
      <OverviewForm task={task} busy={busy} onSave={onSave} />
      <section className="overview-connections" aria-label="Connections">
      <h3>Connections</h3>
      <div className="overview-links">
      <TicketControl task={task} busy={busy} onSave={onTicket} />
      {task.thread_id ? (
        <a className="detail-link codex-link" href={codexURL(task.thread_id)}>
          Open in Codex <span>↗</span>
        </a>
      ) : <TaskChatForm task={task} busy={busy} onLink={onLink} />}
      </div>
      <div className="overview-provenance">
      {task.source_url && (
        <ExternalLink url={task.source_url} label="Open source" />
      )}
      {task.source_task_id && (
        <a
          className="detail-link"
          href={`?task=${encodeURIComponent(task.source_task_id)}`}
        >
          Created by source
        </a>
      )}
      </div>
      </section>
    </div>
  );
}
function PriorityField({ value, onChange }: { value: Priority; onChange: (priority: Priority) => void }) {
  return <Field label="Priority"><select value={value} onChange={(e) => onChange(e.target.value as Priority)}>
    <option value="low">↓ Low</option><option value="normal">— Normal</option><option value="high">↑ High</option>
  </select></Field>;
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
function OverviewForm({ task, busy, onSave }: {
  task: Task; busy: boolean; onSave: (input: unknown) => Promise<Task>;
}) {
  const [baseline, setBaseline] = useState(task);
  const [title, setTitle] = useState(task.title);
  const [description, setDescription] = useState(task.description);
  const [priority, setPriority] = useState<Priority>(task.priority || "normal");
  const [message, setMessage] = useState("");
  useEffect(() => {
    const priorityUnedited = priority === (baseline.priority || "normal");
    const unchanged = task.title === baseline.title && task.description === baseline.description &&
      ((task.priority || "normal") === (baseline.priority || "normal") || priorityUnedited);
    if (unchanged && task.version > baseline.version) {
      if (priorityUnedited) setPriority(task.priority || "normal");
      setBaseline(task);
    }
  }, [task, baseline, priority]);
  const stale = baseline.version !== task.version;
  const reload = () => { setBaseline(task); setTitle(task.title); setDescription(task.description); setPriority(task.priority || "normal"); setMessage(""); };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (stale) { setMessage("Reload latest settings before saving. Your draft has been preserved."); return; }
    try {
      const updated = await onSave({ expected_version: baseline.version, title, description, priority, source_url: baseline.source_url, config: baseline.config });
      const submitted = { ...baseline, title, description, priority };
      const sameOwned = updated.title === title && updated.description === description && (updated.priority || "normal") === priority;
      if (updated.version > baseline.version + 1 && !sameOwned) {
        setBaseline({ ...submitted, version: baseline.version + 1 });
        setMessage("This task changed after saving. Reload latest settings before saving again.");
      } else {
        setBaseline(updated);
        setTitle(current => current === title ? updated.title : current);
        setDescription(current => current === description ? updated.description : current);
        setPriority(current => current === priority ? updated.priority || "normal" : current);
        setMessage("Overview saved.");
      }
    } catch (e) { setMessage((e as Error).message); }
  };
  return <form className="overview-form" onSubmit={e => void save(e)}>
    <div className="overview-heading">
    <Field label="Title" className="overview-title"><textarea required rows={title.length > 70 ? 3 : 2} value={title}
      onChange={e => setTitle(e.target.value.replace(/[\r\n]/g, ""))}
      onKeyDown={e => {
        if (e.key !== "Enter" || e.metaKey || e.ctrlKey || e.repeat || e.nativeEvent.isComposing) return;
        e.preventDefault();
        if (!busy) e.currentTarget.form?.requestSubmit();
      }} /></Field>
    <PriorityField value={priority} onChange={setPriority} />
    </div>
    <Field label="Description" className="overview-description"><textarea rows={3} value={description} onChange={e => setDescription(e.target.value)} /></Field>
    <div className="overview-save">
    {stale && <p className="readiness-banner">This task changed. Reload latest settings before saving. Your draft has been preserved.</p>}
    {stale && <button type="button" disabled={busy} onClick={reload}>Reload latest overview</button>}
    {message && <p role="status" className="info-note">{message}</p>}
    <div className="form-actions overview-footer">
    <p className="note">⌘/Ctrl + Enter saves this section.</p>
    <button className="primary" disabled={busy}>Save overview</button>
    </div>
    </div>
  </form>;
}
function History({ task }: { task: Task }) {
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
    [priority, setPriority] = useState<Priority>("normal"),
    [config, setConfig] = useState(emptyConfig()),
    [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await onSave({ title, description, priority, column: "todo", config });
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
        <PriorityField value={priority} onChange={setPriority} />
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
  onSave: (input: unknown) => Promise<Task>;
}) {
  return (
    <div className="panel-body">
      <h2>Connect a source</h2>
      <SourceEditor
        busy={busy}
        onSave={async (polling, enabled, _version, title) => {
          return onSave({
            title,
            column: enabled ? "polling" : "todo",
            config: emptyConfig(),
            polling,
          });
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
