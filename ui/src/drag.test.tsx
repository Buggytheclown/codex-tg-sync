// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import { defaultPolling, emptyConfig } from "./model";
import type { Task } from "./model";
import { placeTask } from "./taskOrder";

const task = (id: string, extra: Partial<Task> = {}): Task => ({
  id, title: `Task ${id}`, description: "", column: "todo", position: 0,
  thread_id: "", source_url: "", source_key: "", source_task_id: "",
  config: emptyConfig(), version: 1, created_at: "", updated_at: "", requests: [], ...extra,
});
const reply = (payload: unknown, status = 200) => ({ ok: status < 400, status, json: async () => payload });
type Reply = ReturnType<typeof reply>;
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
let rows: Task[], writes: { path: string; body: any }[], invalidate: () => void;
let heldRead: Promise<Reply> | null, heldMove: Promise<Reply> | null;
const transfer = () => ({ setData: vi.fn(), getData: () => "c", effectAllowed: "", dropEffect: "" });
function dragEvent(element: Element, type: string, y: number, dataTransfer: ReturnType<typeof transfer>) {
  fireEvent(element, Object.assign(new Event(type, { bubbles: true, cancelable: true }), { clientY: y, dataTransfer }));
}
function card(id: string) { return document.querySelector<HTMLElement>(`[data-task-id="${id}"]`)!; }
function order(column = "TODO") {
  return Array.from(screen.getByRole("region", { name: `${column} column` })
    .querySelectorAll<HTMLElement>("[data-task-id], .drop-placeholder"))
    .map((element) => element.dataset.taskId || "placeholder");
}
async function startDrag() {
  await screen.findByText("Task c");
  ["a", "b", "c"].forEach((id, i) => vi.spyOn(card(id), "getBoundingClientRect").mockReturnValue({
    top: 100 + i * 100, height: 80, bottom: 180 + i * 100,
    width: 200, left: 0, right: 200, x: 0, y: 100 + i * 100, toJSON() {},
  }));
  const dataTransfer = transfer();
  dragEvent(card("c"), "dragstart", 320, dataTransfer);
  return dataTransfer;
}
beforeEach(() => {
  rows = [task("a"), task("b"), task("c")]; writes = [];
  heldRead = heldMove = null;
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
  vi.stubGlobal("EventSource", class {
    addEventListener(_name: string, listener: () => void) { invalidate = listener; }
    close() {} onopen = null; onerror = null;
  });
  vi.stubGlobal("fetch", vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/v1/tasks") return heldRead || reply(rows);
    if (options?.method === "POST" && path.endsWith("/move")) {
      const body = JSON.parse(options.body as string);
      writes.push({ path, body });
      if (heldMove) return heldMove;
      const current = rows.find((t) => t.id === path.split("/")[3])!;
      rows = placeTask(rows, current, body.column, body.before_task_id);
      rows = rows.map((t) => t.id === current.id ? { ...t, version: t.version + 1 } : t);
      return reply(rows.find((t) => t.id === current.id));
    }
    return reply([]);
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("marks the grabbed card and reserves its height at midpoint insertion; reorders equal positions", async () => {
  render(<App />);
  const dataTransfer = await startDrag();
  expect(card("c").classList.contains("grabbed")).toBe(true);
  expect(screen.getByText("Moving")).toBeTruthy();
  const column = screen.getByRole("region", { name: "TODO column" });
  dragEvent(column, "dragover", 160, dataTransfer);
  expect(order()).toEqual(["a", "placeholder", "b", "c"]);
  expect(screen.getByLabelText("Drop task here").style.minHeight).toBe("80px");
  dragEvent(column, "dragover", 110, dataTransfer);
  expect(order()).toEqual(["placeholder", "a", "b", "c"]);
  dragEvent(column, "drop", 110, dataTransfer);
  await waitFor(() => expect(order()).toEqual(["c", "a", "b"]));
  expect(writes).toEqual([{ path: "/v1/tasks/c/move", body: { expected_version: 1, column: "todo", before_task_id: "a" } }]);
  expect(screen.queryByLabelText("Drop task here")).toBeNull();
  expect(card("c").classList.contains("grabbed")).toBe(false);
  cleanup(); render(<App />);
  await screen.findByText("Task c");
  expect(order()).toEqual(["c", "a", "b"]);
});

it.each(["dragend", "Escape"])("clears grabbed card and placeholder on %s without a write", async (cancel) => {
  render(<App />);
  const dataTransfer = await startDrag();
  const column = screen.getByRole("region", { name: "TODO column" });
  dragEvent(column, "dragover", 110, dataTransfer);
  if (cancel === "Escape") {
    fireEvent.keyDown(window, { key: "Escape" });
    dragEvent(column, "drop", 110, dataTransfer);
  } else dragEvent(card("c"), "dragend", 110, dataTransfer);
  expect(screen.queryByLabelText("Drop task here")).toBeNull();
  expect(card("c").classList.contains("grabbed")).toBe(false);
  expect(writes).toEqual([]);
});

it("keeps optimistic position through old snapshots and settles without awaiting a full-board read", async () => {
  render(<App />);
  await screen.findByText("Task c");
  const oldRead = deferred<Reply>(); heldRead = oldRead.promise;
  act(() => invalidate());
  const move = deferred<Reply>(); heldMove = move.promise;
  const dataTransfer = await startDrag();
  const column = screen.getByRole("region", { name: "TODO column" });
  dragEvent(column, "dragover", 110, dataTransfer);
  dragEvent(column, "drop", 110, dataTransfer);
  expect(order()).toEqual(["c", "a", "b"]);
  expect(card("c").getAttribute("aria-busy")).toBe("true");
  expect(screen.getByText("Saving position…")).toBeTruthy();
  await act(async () => oldRead.resolve(reply(rows)));
  act(() => invalidate());
  expect(order()).toEqual(["c", "a", "b"]);
  const backgroundRead = deferred<Reply>(); heldRead = backgroundRead.promise;
  rows = placeTask(rows, rows[2], "todo", "a");
  const saved = { ...rows.find((t) => t.id === "c")!, version: 2 };
  await act(async () => move.resolve(reply(saved)));
  expect(card("c").getAttribute("aria-busy")).toBe("false");
  expect(screen.queryByText("Saving position…")).toBeNull();
  expect(order()).toEqual(["c", "a", "b"]);
  await act(async () => backgroundRead.resolve(reply(rows)));
  expect(order()).toEqual(["c", "a", "b"]);
});

it.each([500, 409])("rolls back a failed save (%s), reconciles, and retains the actionable error", async (status) => {
  render(<App />);
  const move = deferred<Reply>(); heldMove = move.promise;
  const dataTransfer = await startDrag();
  const column = screen.getByRole("region", { name: "TODO column" });
  dragEvent(column, "dragover", 110, dataTransfer);
  dragEvent(column, "drop", 110, dataTransfer);
  expect(order()).toEqual(["c", "a", "b"]);
  await act(async () => move.resolve(reply({ error: "Move rejected" }, status)));
  expect(order()).toEqual(["a", "b", "c"]);
  expect(screen.getByRole("alert").textContent).toContain(status === 409 ? "retry your move" : "Position was not saved");
  expect(card("c").getAttribute("aria-busy")).toBe("false");
});

it("refreshes queued changes after cancellation and replaces empty-column text with a placeholder", async () => {
  render(<App />);
  const dataTransfer = await startDrag();
  const column = screen.getByRole("region", { name: "Inbox column" });
  dragEvent(column, "dragover", 5, dataTransfer);
  expect(order("Inbox")).toEqual(["placeholder"]);
  expect(screen.queryByText("Nothing incoming")).toBeNull();
  rows = [...rows, task("d")];
  act(() => invalidate());
  expect(screen.queryByText("Task d")).toBeNull();
  dragEvent(card("c"), "dragend", 5, dataTransfer);
  await screen.findByText("Task d");
  expect(screen.getByText("Nothing incoming")).toBeTruthy();
});

it("inserts across columns on valid Polling drop and rejects unconfigured Polling", async () => {
  rows[2] = task("c", { polling: { ...defaultPolling(), config: { pr_id: "123", login: "reviewer", interval_seconds: 60, conditions: ["new_commit"] } } });
  render(<App />);
  const dataTransfer = await startDrag();
  const polling = screen.getByRole("region", { name: "Polling column" });
  dragEvent(polling, "dragover", 5, dataTransfer);
  dragEvent(polling, "drop", 5, dataTransfer);
  await waitFor(() => expect(order("Polling")).toEqual(["c"]));
  await waitFor(() => expect(card("c").getAttribute("aria-busy")).toBe("false"));
  expect(writes[0].body).toEqual({ expected_version: 1, column: "polling", before_task_id: "" });
  dragEvent(card("a"), "dragstart", 5, dataTransfer);
  dragEvent(polling, "dragover", 5, dataTransfer);
  dragEvent(polling, "drop", 5, dataTransfer);
  await screen.findByRole("tab", { name: "Polling", selected: true });
  expect(writes).toHaveLength(1);
  expect(screen.queryByLabelText("Drop task here")).toBeNull();
});

it("inserts into the previewed gap of another populated column", async () => {
  rows[2] = task("c", { column: "inbox" });
  render(<App />);
  const dataTransfer = await startDrag();
  const column = screen.getByRole("region", { name: "TODO column" });
  dragEvent(column, "dragover", 160, dataTransfer);
  expect(order()).toEqual(["a", "placeholder", "b"]);
  dragEvent(column, "drop", 160, dataTransfer);
  await waitFor(() => expect(order()).toEqual(["a", "c", "b"]));
  expect(writes[0].body.before_task_id).toBe("b");
  expect(order("Inbox")).toEqual([]);
});

it("reorders an existing Polling card even when its stored configuration needs attention", async () => {
  rows = rows.map((t) => ({ ...t, column: "polling", polling: defaultPolling() }));
  render(<App />);
  const dataTransfer = await startDrag();
  const column = screen.getByRole("region", { name: "Polling column" });
  dragEvent(column, "dragover", 110, dataTransfer);
  dragEvent(column, "drop", 110, dataTransfer);
  await waitFor(() => expect(order("Polling")).toEqual(["c", "a", "b"]));
  expect(writes).toHaveLength(1);
  expect(screen.queryByRole("tab", { name: "Polling" })).toBeNull();
});

it.each(["held", "rejected"])(
  "keeps optimistic ordering when the server rank includes an unseen card and reconciliation is %s",
  async (readState) => {
    render(<App />);
    const move = deferred<Reply>();
    heldMove = move.promise;
    const dataTransfer = await startDrag();
    const column = screen.getByRole("region", { name: "TODO column" });
    dragEvent(column, "dragover", 110, dataTransfer);
    dragEvent(column, "drop", 110, dataTransfer);
    expect(order()).toEqual(["c", "a", "b"]);

    // A source creates a card while the browser's snapshot is frozen. The
    // server's moved-card rank now differs from the visible optimistic ranks.
    const saved = task("c", {
      title: "Server C",
      description: "Updated on the server",
      version: 2,
      position: 2048,
    });
    rows = [
      task("0", { title: "New source task", position: 1024 }),
      saved,
      task("a", { position: 3072 }),
      task("b", { position: 4096 }),
    ];
    const snapshot = deferred<Reply>();
    heldRead = snapshot.promise;
    await act(async () => move.resolve(reply(saved)));
    expect(card("c").getAttribute("aria-busy")).toBe("false");
    expect(screen.getByText("Server C")).toBeTruthy();
    expect(screen.getByText("Updated on the server")).toBeTruthy();
    expect(order()).toEqual(["c", "a", "b"]);

    if (readState === "rejected") {
      await act(async () => snapshot.resolve(reply({ error: "Snapshot unavailable" }, 503)));
      expect(order()).toEqual(["c", "a", "b"]);
      heldRead = null;
      act(() => invalidate());
    } else {
      await act(async () => snapshot.resolve(reply(rows)));
    }
    await screen.findByText("New source task");
    expect(order()).toEqual(["0", "c", "a", "b"]);
  },
);
