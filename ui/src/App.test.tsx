// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  within,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { App } from "./App";
import { WorkflowForm } from "./Workflow";
import { TicketControl } from "./TicketControl";
import { compactTask, defaultPolling, emptyConfig } from "./model";
import type { Task, Polling } from "./model";
const makeTask = (extra: Partial<Task> = {}): Task => ({
  id: "task-1",
  title: "Review the next revision",
  description: "Check comments",
  column: "todo",
  position: 1,
  thread_id: "thread-1",
  source_url: "",
  source_key: "",
  source_task_id: "",
  config: emptyConfig(),
  version: 1,
  created_at: "",
  updated_at: "",
  requests: [],
  ...extra,
});
let rows: Task[], calls: { path: string; method: string; body: unknown }[];
it("places the editable title in the shared heading above all three tabs", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title") as HTMLTextAreaElement;
  const heading = title.closest(".detail-heading");
  expect(heading).not.toBeNull();
  expect(title.form).toBe((screen.getByLabelText("Description") as HTMLTextAreaElement).form);
  for (const tab of ["Polling", "History", "Overview"]) {
    fireEvent.click(screen.getByRole("tab", { name: tab }));
    const current = screen.getByRole("heading", { name: rows[0].title });
    expect(current.closest(".detail-heading")).toBe(heading);
  }
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it.each(["blur", "Enter", "CmdEnter"])("saves the header title with %s through the same Overview form", async trigger => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  expect(title.closest(".detail-heading")).not.toBeNull();
  fireEvent.change(title, { target: { value: "Edited in the header" } });
  if (trigger === "blur") fireEvent.blur(title);
  else fireEvent.keyDown(title, { key: "Enter", metaKey: trigger === "CmdEnter" });
  await waitFor(() => expect(rows[0].title).toBe("Edited in the header"));
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(1);
  expect(rows[0].description).toBe("Check comments");
});
it("shows a noninteractive empty inspector when no card is selected", async () => {
  render(<App />);
  await screen.findByText("Select a card to see its details.");
  const panel = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  expect(panel.queryByRole("button")).toBeNull();
  expect(panel.queryByRole("textbox")).toBeNull();
  expect(panel.queryByRole("tab")).toBeNull();
  expect(panel.queryByRole("status")).toBeNull();
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("dismisses details with Escape, clears only the task URL parameter and reopens the card", async () => {
  window.history.replaceState(null, "", "/?task=task-1&view=board#queue");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.keyDown(title, { key: "Escape", isComposing: true });
  expect(screen.getByLabelText("Title")).toBeTruthy();
  fireEvent.keyDown(title, { key: "Escape" });
  await screen.findByText("Select a card to see its details.");
  expect(window.location.search).toBe("?view=board");
  expect(window.location.hash).toBe("#queue");
  expect(screen.queryByRole("textbox", { name: "Title" })).toBeNull();
  expect(screen.getByRole("button", { name: /CHAT Review the next revision/ }).getAttribute("aria-pressed")).toBe("false");
  fireEvent.click(screen.getByRole("button", { name: /CHAT Review the next revision/ }));
  await screen.findByLabelText("Title");
  expect(new URLSearchParams(window.location.search).get("task")).toBe("task-1");
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("dismisses on an outside click while preserving inside clicks and destination actions", async () => {
  rows.push(makeTask({ id: "task-2", title: "Other task" }));
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  await screen.findByLabelText("Title");
  fireEvent.click(screen.getByLabelText("Description"));
  expect(screen.getByLabelText("Title")).toBeTruthy();
  fireEvent.click(document.body);
  await screen.findByText("Select a card to see its details.");
  fireEvent.click(screen.getByRole("button", { name: /CHAT Review the next revision/ }));
  await screen.findByLabelText("Title");
  fireEvent.click(screen.getByRole("button", { name: /CHAT Other task/ }));
  await screen.findByDisplayValue("Other task");
  expect(new URLSearchParams(window.location.search).get("task")).toBe("task-2");
  fireEvent.click(screen.getByRole("button", { name: "＋ New task" }));
  await screen.findByText("Something to work on");
  fireEvent.keyDown(window, { key: "Escape" });
  expect(screen.getByText("Something to work on")).toBeTruthy();
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("blurs and saves Overview on Escape without reopening details when the save completes", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH") await held;
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  title.focus();
  fireEvent.change(title, { target: { value: "Saved while dismissed" } });
  fireEvent.keyDown(title, { key: "Escape" });
  await screen.findByText("Select a card to see its details.");
  release();
  await waitFor(() => expect(rows[0].title).toBe("Saved while dismissed"));
  expect(screen.queryByRole("textbox", { name: "Title" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /CHAT Saved while dismissed/ }));
  await screen.findByDisplayValue("Saved while dismissed");
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(1);
});
it("retains unsaved Polling settings and its tab when reopening the dismissed card", async () => {
  rows[0].polling = defaultPolling();
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  await screen.findByLabelText("Title");
  fireEvent.click(screen.getByRole("tab", { name: "Polling" }));
  const prID = screen.getByLabelText("PR ID");
  fireEvent.change(prID, { target: { value: "456" } });
  fireEvent.keyDown(prID, { key: "Escape" });
  await screen.findByText("Select a card to see its details.");
  fireEvent.click(screen.getByRole("button", { name: /CHAT Review the next revision/ }));
  await screen.findByRole("tab", { name: "Polling", selected: true });
  expect((screen.getByLabelText("PR ID") as HTMLInputElement).value).toBe("456");
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("keeps failed Overview drafts reachable after dismissal", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (path, options) => options?.method === "PATCH"
    ? { ok: false, status: 503, json: async () => ({ error: "Overview temporarily unavailable" }) } as Response
    : original(path, options));
  render(<App />);
  const title = await screen.findByLabelText("Title");
  title.focus();
  fireEvent.change(title, { target: { value: "Retained on dismissal" } });
  fireEvent.keyDown(title, { key: "Escape" });
  const reopen = await screen.findByRole("button", { name: "Open failed task" });
  fireEvent.click(reopen);
  await screen.findByRole("button", { name: "Retry overview save" });
  expect((screen.getByLabelText("Title") as HTMLTextAreaElement).value).toBe("Retained on dismissal");
  expect(vi.mocked(fetch).mock.calls.filter(([, options]) => options?.method === "PATCH")).toHaveLength(1);
});
it("retains a source's unsaved child definition across outside dismissal", async () => {
  rows = [makeTask({
    id: "source", title: "PR source", thread_id: "",
    polling: { ...defaultPolling("pr_review_source"), action: {
      type: "create_task", child_definition: { title: "Child task", description: "", config: emptyConfig() },
    } },
  })];
  window.history.replaceState(null, "", "/?task=source");
  render(<App />);
  const childTitle = await screen.findByLabelText("Task title");
  fireEvent.change(childTitle, { target: { value: "Unsaved child title" } });
  fireEvent.click(document.body);
  await screen.findByText("Select a card to see its details.");
  fireEvent.click(screen.getByRole("button", { name: /PR source/ }));
  expect((await screen.findByLabelText("Task title") as HTMLInputElement).value).toBe("Unsaved child title");
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("ignores detail responses that finish after dismissal", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (path === "/v1/tasks/task-1") await held;
    return original(path, options);
  });
  render(<App />);
  await screen.findByText("Loading details…");
  fireEvent.keyDown(window, { key: "Escape" });
  release();
  await screen.findByText("Select a card to see its details.");
  await waitFor(() => expect(calls.some(call => call.path === "/v1/tasks/task-1")).toBe(true));
  expect(screen.queryByRole("textbox", { name: "Title" })).toBeNull();
  expect(new URLSearchParams(window.location.search).has("task")).toBe(false);
});
it("autosaves changed Overview fields on blur without an explicit save button or unchanged writes", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  expect(screen.queryByRole("button", { name: "Save overview" })).toBeNull();
  fireEvent.blur(title);
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(0);
  fireEvent.change(title, { target: { value: "Autosaved title" } });
  fireEvent.blur(title);
  await screen.findByText("Overview saved.");
  expect(rows[0].title).toBe("Autosaved title");
  fireEvent.blur(title);
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(1);
  const priority = screen.getByLabelText("Priority");
  fireEvent.change(priority, { target: { value: "high" } });
  fireEvent.blur(priority);
  await waitFor(() => expect(rows[0].priority).toBe("high"));
});
it.each(["", "   "])("shows a required-title error and retains the draft when autosaving %j", async value => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value } });
  fireEvent.blur(title);
  await screen.findByText("Enter a title before saving Overview.");
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(0);
  expect((title as HTMLTextAreaElement).value).toBe(value);
});
it.each(["tab", "card"])("serializes Overview edits blurred while saving across a %s switch", async destination => {
  rows.push(makeTask({ id: "task-2", title: "Other task" }));
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  let patches = 0;
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH" && ++patches === 1) await held;
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "First saved draft" } });
  fireEvent.blur(title);
  await screen.findByText("Saving overview…");
  fireEvent.change(title, { target: { value: "Latest queued draft" } });
  const description = screen.getByLabelText("Description");
  fireEvent.change(description, { target: { value: "Queued description\nStill multiline" } });
  fireEvent.blur(description);
  if (destination === "tab") fireEvent.click(screen.getByRole("tab", { name: "Polling" }));
  else fireEvent.click(screen.getByRole("button", { name: /CHAT Other task/ }));
  await waitFor(() => expect(screen.queryByDisplayValue("Latest queued draft")).toBeNull());
  release();
  await waitFor(() => expect(rows[0].title).toBe("Latest queued draft"));
  expect(rows[0].description).toBe("Queued description\nStill multiline");
  expect(calls.filter(call => call.method === "PATCH").map(call => call.body)).toEqual([
    expect.objectContaining({ expected_version: 1, title: "First saved draft" }),
    expect.objectContaining({ expected_version: 2, title: "Latest queued draft" }),
  ]);
  expect(rows[1].title).toBe("Other task");
});
it("keeps a failed Overview draft across navigation and retries explicitly", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let fail = true;
  vi.mocked(fetch).mockImplementation(async (path, options) => options?.method === "PATCH" && fail
    ? { ok: false, status: 503, json: async () => ({ error: "Overview temporarily unavailable" }) } as Response
    : original(path, options));
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "Retained failed draft" } });
  fireEvent.blur(title);
  await screen.findByRole("button", { name: "Retry overview save" });
  const writes = vi.mocked(fetch).mock.calls.filter(([, options]) => options?.method === "PATCH").length;
  fireEvent.focus(title);
  fireEvent.blur(title);
  expect(vi.mocked(fetch).mock.calls.filter(([, options]) => options?.method === "PATCH")).toHaveLength(writes);
  fireEvent.click(screen.getByRole("tab", { name: "History" }));
  fireEvent.click(screen.getByRole("tab", { name: "Overview" }));
  expect((await screen.findByLabelText("Title") as HTMLTextAreaElement).value).toBe("Retained failed draft");
  fail = false;
  fireEvent.click(screen.getByRole("button", { name: "Retry overview save" }));
  await waitFor(() => expect(rows[0].title).toBe("Retained failed draft"));
});
it("keeps the first ticket click available during a blur save and serializes its mutation", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH") await held;
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  const detail = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  fireEvent.change(title, { target: { value: "Blurred before ticket" } });
  fireEvent.blur(title);
  const add = detail.getByRole("button", { name: "Add ticket" });
  expect((add as HTMLButtonElement).disabled).toBe(false);
  fireEvent.click(add);
  fireEvent.change(detail.getByLabelText("Ticket URL"), { target: { value: "TASK-123" } });
  fireEvent.click(detail.getByRole("button", { name: "Save ticket" }));
  expect(calls.some(call => call.path.endsWith("/ticket") && call.method === "POST")).toBe(false);
  release();
  await waitFor(() => expect(rows[0].ticket_url).toBe("https://st.yandex-team.ru/TASK-123"));
  expect(calls.find(call => call.path.endsWith("/ticket") && call.method === "POST")?.body).toMatchObject({ expected_version: 2 });
  expect(rows[0].title).toBe("Blurred before ticket");
});
it("saves a blur queued during another command after navigating away", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (String(path).endsWith("/priority") && options?.method === "POST") await held;
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(screen.getByLabelText("Priority for Review the next revision"), { target: { value: "low" } });
  fireEvent.change(title, { target: { value: "Queued while command busy" } });
  fireEvent.blur(title);
  fireEvent.click(screen.getByRole("tab", { name: "History" }));
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(0);
  release();
  await waitFor(() => expect(rows[0].title).toBe("Queued while command busy"));
  expect(rows[0].priority).toBe("low");
  expect(calls.find(call => call.method === "PATCH")?.body).toMatchObject({ expected_version: 2, priority: "low" });
});
it("keeps autosaved text when a workflow save waits for the same task", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  let patches = 0;
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH" && ++patches === 1) await held;
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "Autosaved before workflow" } });
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Autosaved description" } });
  fireEvent.blur(title);
  fireEvent.click(screen.getByRole("tab", { name: "Polling" }));
  fireEvent.change(screen.getByLabelText("Initial prompt"), { target: { value: "Workflow draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(patches).toBe(1);
  release();
  await waitFor(() => expect(rows[0].config.initial_prompt).toBe("Workflow draft"));
  expect(rows[0].title).toBe("Autosaved before workflow");
  expect(rows[0].description).toBe("Autosaved description");
  expect(calls.filter(call => call.method === "PATCH")[1].body).toMatchObject({ expected_version: 2, title: "Autosaved before workflow", description: "Autosaved description" });
});
it("uses the owned autosave version for the first Move command", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH") await held;
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "Moved saved task" } });
  fireEvent.blur(title);
  const move = screen.getByLabelText("Move task");
  expect((move as HTMLSelectElement).disabled).toBe(false);
  fireEvent.change(move, { target: { value: "inbox" } });
  expect(calls.some(call => call.path.endsWith("/move") && call.method === "POST")).toBe(false);
  release();
  await waitFor(() => expect(rows[0].column).toBe("inbox"));
  expect(rows[0].title).toBe("Moved saved task");
  expect(calls.find(call => call.path.endsWith("/move") && call.method === "POST")?.body).toMatchObject({ expected_version: 2 });
});
it("does not run a waiting ticket command when autosave fails", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH") {
      await held;
      return { ok: false, status: 503, json: async () => ({ error: "Autosave failed before ticket" }) } as Response;
    }
    return original(path, options);
  });
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Unsaved failed title" } });
  fireEvent.blur(screen.getByLabelText("Title"));
  const detail = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  fireEvent.click(detail.getByRole("button", { name: "Add ticket" }));
  fireEvent.change(detail.getByLabelText("Ticket URL"), { target: { value: "TASK-123" } });
  fireEvent.click(detail.getByRole("button", { name: "Save ticket" }));
  release();
  await detail.findByRole("button", { name: "Retry overview save" });
  expect(calls.some(call => call.path.endsWith("/ticket") && call.method === "POST")).toBe(false);
  expect((detail.getByLabelText("Ticket URL") as HTMLInputElement).value).toBe("TASK-123");
});
it("coalesces a repeated blur of a server-normalized title and advances a waiting ticket", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH") {
      await held;
      await original(path, options);
      rows = rows.map(task => ({ ...task, title: task.title.trim() }));
      return { ok: true, status: 200, json: async () => rows[0] } as Response;
    }
    return original(path, options);
  });
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "  Normalized title  " } });
  fireEvent.blur(title);
  fireEvent.blur(title);
  const detail = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  fireEvent.click(detail.getByRole("button", { name: "Add ticket" }));
  fireEvent.change(detail.getByLabelText("Ticket URL"), { target: { value: "TASK-123" } });
  fireEvent.click(detail.getByRole("button", { name: "Save ticket" }));
  release();
  await waitFor(() => expect(rows[0].ticket_url).toBe("https://st.yandex-team.ru/TASK-123"));
  expect(rows[0].title).toBe("Normalized title");
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(1);
  expect(calls.find(call => call.path.endsWith("/ticket") && call.method === "POST")?.body).toMatchObject({ expected_version: 2 });
});
it("identifies an old task's failed autosave after selecting another card and restores its draft", async () => {
  rows.push(makeTask({ id: "task-2", title: "Other task" }));
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let release!: () => void;
  const held = new Promise<void>(done => { release = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "PATCH") {
      await held;
      return { ok: false, status: 503, json: async () => ({ error: "Original task save failed" }) } as Response;
    }
    return original(path, options);
  });
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Keep original draft" } });
  fireEvent.blur(screen.getByLabelText("Title"));
  fireEvent.click(screen.getByRole("button", { name: /CHAT Other task/ }));
  await screen.findByDisplayValue("Other task");
  release();
  await screen.findByText("Review the next revision: Original task save failed");
  expect((screen.getByLabelText("Title") as HTMLTextAreaElement).value).toBe("Other task");
  expect(rows[1].version).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "Open failed task" }));
  expect((await screen.findByLabelText("Title") as HTMLTextAreaElement).value).toBe("Keep original draft");
  expect(screen.getByRole("button", { name: "Retry overview save" })).toBeTruthy();
});
it("keeps Overview a native editable brief while polling diagnostics and frozen requests remain in their tabs", async () => {
  rows = [makeTask({
    title: "A readable task brief", description: "First line\nSecond line", priority: "high",
    source_url: "https://example.com/source", source_task_id: "source-1",
    created_at: "2026-01-01T12:00:00Z", updated_at: "2026-01-02T12:00:00Z",
    execution: { last_seen_turn_status: "completed", last_poll_at: "2026-01-03T12:00:00Z" },
    polling: { ...defaultPolling(), config: { ...defaultPolling().config, pr_id: "42" }, last_attempt_at: "2026-01-04T12:00:00Z" },
    requests: [{ id: "request-brief", status: "pending_approval", prompt: "Preserved frozen instruction", created_at: "2026-01-05T12:00:00Z" }],
  })];
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  const detail = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  expect(title.tagName).toBe("TEXTAREA");
  expect(detail.getByLabelText("Description").tagName).toBe("TEXTAREA");
  expect((detail.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("First line\nSecond line");
  expect(detail.getByRole("heading", { name: rows[0].title })).toBeTruthy();
  for (const label of ["Waiting conditions · ANY", "Next check", "Last attempt", "Last success", "Created", "Updated", "Chat read", "Launch requests", "Frozen prompt"]) {
    expect(detail.queryByText(label)).toBeNull();
  }
  expect(detail.getByRole("link", { name: /Open source/ }).getAttribute("href")).toBe("https://example.com/source");
  expect(detail.getByRole("link", { name: "Created by source" }).getAttribute("href")).toBe("?task=source-1");
  fireEvent.click(detail.getByRole("tab", { name: "Polling" }));
  expect(detail.getByLabelText("PR ID")).toBeTruthy();
  expect(detail.getByText("Last attempt")).toBeTruthy();
  expect(detail.getByLabelText("Move to Done when PR is merged")).toBeTruthy();
  fireEvent.click(detail.getByRole("tab", { name: "History" }));
  expect(await detail.findByText("Frozen prompt")).toBeTruthy();
  expect(detail.getByText("Preserved frozen instruction")).toBeTruthy();
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("offers one card priority control and separate keyboard targets for ticket and Codex", async () => {
  rows = [makeTask({ ticket_url: "https://example.com/TASK-123", priority: "high" })];
  render(<App />);
  const select = await screen.findByLabelText("Priority for Review the next revision");
  const wrapper = select.closest<HTMLElement>(".card-with-link")!;
  const card = within(wrapper).getByRole("button", { name: /CHAT Review the next revision/ });
  expect(within(wrapper).getAllByRole("combobox")).toHaveLength(1);
  expect(card.querySelector("a, select, input, textarea, button")).toBeNull();
  expect(within(wrapper).queryByRole("img", { name: "High priority" })).toBeNull();
  for (const target of [select, within(wrapper).getByRole("link", { name: "TASK-123" }), within(wrapper).getByRole("button", { name: "Edit ticket" }), within(wrapper).getByRole("link", { name: /Open in Codex/ })]) {
    expect(target.closest(".task-card")).toBeNull();
    target.focus();
    expect(document.activeElement).toBe(target);
  }
  expect(screen.queryByLabelText("Title")).toBeNull();
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("preserves title Enter submission and native validation without consuming IME composition", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "" } });
  fireEvent.keyDown(title, { key: "Enter" });
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(0);
  fireEvent.change(title, { target: { value: "A wrapped title saved with Enter" } });
  expect(fireEvent.keyDown(title, { key: "Enter", isComposing: true })).toBe(true);
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(0);
  fireEvent.keyDown(title, { key: "Enter" });
  await waitFor(() => expect(rows[0].title).toBe("A wrapped title saved with Enter"));
  expect(calls.filter(call => call.method === "PATCH")).toHaveLength(1);
});
it("keeps an existing multiline title unchanged when saving another Overview field", async () => {
  rows = [makeTask({ title: "Existing first line\nExisting second line" })];
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  expect((await screen.findByLabelText("Title") as HTMLTextAreaElement).value).toBe(rows[0].title);
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "A changed brief\nWith a second line" } });
  fireEvent.blur(screen.getByLabelText("Title"));
  await waitFor(() => expect(rows[0].description).toBe("A changed brief\nWith a second line"));
  expect(rows[0].title).toBe("Existing first line\nExisting second line");
});
it("shows the Telegram gate for an actual pending approval request", async () => {
  rows = [
    makeTask({
      column: "inbox",
      requests: [
        {
          id: "request-1",
          status: "pending_approval",
          prompt: "Review",
          created_at: "",
        },
      ],
    }),
  ];
  render(<App />);
  expect(await screen.findByText("Launch request · Telegram")).toBeTruthy();
});
beforeEach(() => {
  rows = [makeTask()];
  calls = [];
  window.history.replaceState(null, "", "/");
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener() {}
      close() {}
      onopen = null;
      onerror = null;
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      const method = options?.method || "GET";
      calls.push({
        path,
        method,
        body: options?.body ? JSON.parse(options.body as string) : undefined,
      });
      const body = options?.body
        ? JSON.parse(options.body as string)
        : undefined;
      let payload: unknown =
        path === "/v1/task-cards" ? rows.map(compactTask) : path.endsWith("/events") ? [] : rows.find((t) => path === `/v1/tasks/${t.id}`) || {};
      if (method === "PATCH") {
        const task = rows.find((t) => path === `/v1/tasks/${t.id}`)!;
        payload = { ...task, ...body, version: task.version + 1 };
        rows = rows.map((t) => t.id === task.id ? payload as Task : t);
      }
      if (method === "PUT" && path.endsWith("/polling")) {
        const id = path.split("/")[3];
        const task = rows.find((t) => t.id === id)!;
        payload = { ...task, polling: body.polling, version: task.version + 1 };
        rows = rows.map((t) => (t.id === id ? (payload as Task) : t));
      }
      if (method === "POST" && path.endsWith("/ticket")) {
        const task = rows.find(t => path === `/v1/tasks/${t.id}/ticket`)!;
        const saved = { ...task, ticket_url: body.ticket_url, version: task.version + 1 };
        rows = rows.map(t => t.id === task.id ? saved : t);
        payload = compactTask(saved);
      }
      if (method === "POST" && path.endsWith("/priority")) {
        const task = rows.find(t => path === `/v1/tasks/${t.id}/priority`)!;
        const saved = { ...task, priority: body.priority, version: task.version + 1 };
        rows = rows.map(t => t.id === task.id ? saved : t);
        payload = compactTask(saved);
      }
      if (method === "POST" && path.endsWith("/chat")) {
        const task = rows.find(t => path === `/v1/tasks/${t.id}/chat`)!;
        payload = { ...task, thread_id: body.thread_id, version: task.version + 1 };
        rows = rows.map(t => t.id === task.id ? payload as Task : t);
      }
      if (method === "POST" && path === "/v1/tasks") {
        payload = makeTask({ id: "new-source", ...body });
        rows = [...rows, payload as Task];
      }
      if (method === "POST" && (path.endsWith("/move") || path.endsWith("/reorder"))) {
        const id = path.split("/")[3];
        const task = rows.find((t) => t.id === id)!;
        payload = { ...task, column: body.column, version: task.version + 1 };
        rows = rows.map((t) => (t.id === id ? (payload as Task) : t));
      }
      return { ok: true, status: 200, json: async () => payload };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
describe("board operator flows", () => {
  it("deep links to a task without moving it or starting work", async () => {
    window.history.replaceState(null, "", "/?task=task-1");
    render(<App />);
    await screen.findByRole("combobox", { name: "Move task" });
    expect(calls.every((c) => c.method === "GET")).toBe(true);
    expect(screen.queryByRole("button", { name: "Start" })).toBeNull();
    expect(screen.queryByText("Completed")).toBeNull();
  });
  it("opens the polling form instead of moving an unconfigured task", async () => {
    render(<App />);
    const card = await screen.findByRole("button", {
      name: /CHAT Review the next revision/,
    });
    fireEvent.click(card);
    await screen.findByRole("combobox", { name: "Move task" });
    fireEvent.change(screen.getByRole("combobox", { name: "Move task" }), {
      target: { value: "polling" },
    });
    await screen.findByText(
      "Add a polling configuration before moving to Polling.",
    );
    expect(
      screen
        .getByRole("tab", { name: "Polling" })
        .getAttribute("aria-selected"),
    ).toBe("true");
    expect(calls.some((c) => c.method !== "GET")).toBe(false);
    expect(
      screen.getByRole("button", { name: "Activate waiting" }),
    ).toBeTruthy();
  });
  it("routes an invalid Polling drop into configuration without a write", async () => {
    render(<App />);
    await screen.findByRole("button", {
      name: /CHAT Review the next revision/,
    });
    fireEvent.drop(screen.getByRole("region", { name: "Polling column" }), {
      dataTransfer: { getData: () => rows[0].id },
    });
    await screen.findByRole("tab", { name: "Polling", selected: true });
    expect(calls.some((c) => c.method !== "GET")).toBe(false);
  });
  it("keeps paused sources visible and supports collapsing them", async () => {
    rows = [
      makeTask({
        id: "source",
        title: "Assigned reviews",
        column: "done",
        polling: {
          type: "pr_review_source",
          mode: "repeat",
          config: { login: "me", interval_seconds: 60 },
          action: {
            type: "create_task",
            child_definition: {
              title: "Review PR",
              description: "",
              config: { ...emptyConfig(), initial_prompt: "Review the PR" },
            },
          },
        },
      }),
    ];
    render(<App />);
    await screen.findByText("Paused · Done");
    fireEvent.click(screen.getByRole("button", { name: "Collapse sources" }));
    expect(screen.queryByText("Paused · Done")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Expand sources" }));
    expect(screen.getByText("Paused · Done")).toBeTruthy();
  });
  it("shows only a source's actual child definition on one page", async () => {
    rows = [sourceTask()];
    window.history.replaceState(null, "", "/?task=source");
    render(<App />);
    await screen.findByRole("heading", { name: "Create task" });
    expect(
      screen.getByRole("heading", { name: "When" }),
    ).toBeTruthy();
    expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe(
      "Use $scan-internal-radar",
    );
    expect(
      (screen.getByLabelText("Working directory") as HTMLInputElement).value,
    ).toBe("/workspace/journalist");
    expect(screen.queryByRole("tab")).toBeNull();
    expect(screen.queryByRole("button", { name: /Templates/ })).toBeNull();
    expect(calls.some((c) => c.path.includes("template"))).toBe(false);
    expect(screen.queryByDisplayValue("unused source prompt")).toBeNull();
    expect(screen.queryByDisplayValue("/workspace/remote_debugger")).toBeNull();
  });
  it("creates a source directly with its child definition", async () => {
    render(<App />);
    await screen.findByRole("button", {
      name: /CHAT Review the next revision/,
    });
    fireEvent.click(screen.getByRole("button", { name: /Add source/ }));
    fireEvent.change(screen.getByLabelText("Source name"), {
      target: { value: "My reviews" },
    });
    expect(screen.queryByLabelText("Your Arcanum login")).toBeNull();
    fireEvent.change(screen.getByLabelText("Task title"), {
      target: { value: "Review {{pr_id}}" },
    });
    fireEvent.change(screen.getByLabelText("Initial prompt"), {
      target: { value: "Check {{pr_url}}" },
    });
    fireEvent.change(screen.getByLabelText("Working directory"), {
      target: { value: "/workspace/review" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create source" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      title: "My reviews",
      column: "todo",
      polling: {
        action: {
          type: "create_task",
          child_definition: {
            title: "Review {{pr_id}}",
            config: {
              initial_prompt: "Check {{pr_url}}",
              execution: { cwd: "/workspace/review" },
            },
          },
        },
      },
    });
    expect(JSON.stringify(calls)).not.toContain("template");
  });
  it("saves trigger and child settings together without changing existing children", async () => {
    rows = [
      sourceTask(),
      makeTask({ id: "child", source_task_id: "source", column: "inbox" }),
    ];
    window.history.replaceState(null, "", "/?task=source");
    render(<App />);
    await screen.findByLabelText("Initial prompt");
    fireEvent.change(screen.getByLabelText("Initial prompt"), {
      target: { value: "Future instruction" },
    });
    fireEvent.change(screen.getByLabelText("Check every · seconds"), {
      target: { value: "120" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save source" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const writes = calls.filter((c) => c.method !== "GET");
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatchObject({
      path: "/v1/tasks/source/polling",
      body: {
        expected_version: 3,
        polling: {
          config: { interval_seconds: 120 },
          action: {
            child_definition: {
              config: { initial_prompt: "Future instruction" },
            },
          },
        },
      },
    });
    expect(rows.find((t) => t.id === "child")?.config.initial_prompt).toBe("");
  });
  it("links a generated task back to its source without template metadata", async () => {
    rows = [makeTask({ source_task_id: "source" })];
    window.history.replaceState(null, "", "/?task=task-1");
    render(<App />);
    expect(
      (
        await screen.findByRole("link", { name: "Created by source" })
      ).getAttribute("href"),
    ).toBe("?task=source");
    expect(screen.queryByText(/Template snapshot/)).toBeNull();
  });
});
function sourceTask(): Task {
  return makeTask({
    id: "source",
    title: "Monday radar",
    column: "polling",
    version: 3,
    thread_id: "",
    config: {
      ...emptyConfig(),
      initial_prompt: "unused source prompt",
      execution: {
        ...emptyConfig().execution,
        cwd: "/workspace/remote_debugger",
      },
    },
    polling: {
      type: "pr_review_source",
      mode: "repeat",
      config: { login: "reviewer", interval_seconds: 60 },
      action: {
        type: "create_task",
        child_definition: {
          title: "Review {{pr_id}}",
          description: "",
          config: {
            ...emptyConfig(),
            initial_prompt: "Use $scan-internal-radar",
            execution: {
              ...emptyConfig().execution,
              cwd: "/workspace/journalist",
            },
          },
        },
      },
    },
  });
}
it("retains a source draft while adopting a version with unchanged owned settings", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const original = sourceTask();
  const save = vi.fn(
    async (polling: Polling, _enabled: boolean, version: number) => makeTask({ polling, version: version + 1 }),
  );
  const view = render(
    <SourceEditor task={original} busy={false} onSave={save} />,
  );
  fireEvent.change(screen.getByLabelText("Initial prompt"), {
    target: { value: "My draft" },
  });
  view.rerender(
    <SourceEditor
      task={{ ...original, version: 4 }}
      busy={false}
      onSave={save}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][2]).toBe(4);
  expect(save.mock.calls[0][0]).toMatchObject({
    action: { child_definition: { config: { initial_prompt: "My draft" } } },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][2]).toBe(5);
});

it("pauses a source independently while preserving its unsaved child draft", async () => {
  rows = [sourceTask()];
  window.history.replaceState(null, "", "/?task=source");
  render(<App />);
  await screen.findByLabelText("Initial prompt");
  fireEvent.change(screen.getByLabelText("Initial prompt"), {
    target: { value: "Unsaved future prompt" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Pause source" }));
  await screen.findByRole("button", { name: "Enable source" });
  expect(calls.filter((c) => c.method !== "GET")).toEqual([
    {
      path: "/v1/tasks/source/move",
      method: "POST",
      body: { expected_version: 3, column: "todo", position: 1 },
    },
  ]);
  expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe(
    "Unsaved future prompt",
  );
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
  expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
    expected_version: 4,
  });
  expect(rows[0].column).toBe("todo");
});
it("copies optional waiting defaults inactive with one continuation instruction", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const save = vi.fn(async (polling: Polling, _enabled: boolean, version: number) => makeTask({ polling, version: version + 1 }));
  render(<SourceEditor task={sourceTask()} busy={false} onSave={save} />);
  fireEvent.click(screen.getByLabelText("Waiting settings for new tasks"));
  fireEvent.change(screen.getByLabelText("Continuation prompt"), {
    target: { value: "Review the new revision" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][0]).toMatchObject({
    action: {
      child_definition: {
        config: { continuation_prompt: "Review the new revision" },
        polling: {
          type: "pr_wait",
          mode: "once",
          action: { type: "resume_task" },
          config: { pr_id: "{{pr_id}}" },
        },
      },
    },
  });
});

it("preserves a PR waiting draft when switching to cron and requires explicit PR values", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const t = sourceTask();
  t.polling!.action.child_definition!.config.continuation_prompt =
    "Keep this follow-up";
  t.polling!.action.child_definition!.polling = {
    ...defaultPolling(),
    config: {
      login: "reviewer",
      pr_id: "{{pr_id}}",
      author: "{{author}}",
      conditions: ["new_commit"],
      interval_seconds: 60,
    },
  };
  const save = vi.fn(async (polling: Polling, _enabled: boolean, version: number) => makeTask({ polling, version: version + 1 }));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  fireEvent.change(screen.getAllByLabelText("Polling type")[0], {
    target: { value: "cron" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  expect(await screen.findByText(/explicit positive PR ID/)).toBeTruthy();
  expect(save).not.toHaveBeenCalled();
  expect((screen.getByLabelText("PR ID") as HTMLInputElement).value).toBe(
    "{{pr_id}}",
  );
  expect(
    (screen.getByLabelText("Continuation prompt") as HTMLInputElement).value,
  ).toBe("Keep this follow-up");
  fireEvent.change(screen.getByLabelText("PR ID"), {
    target: { value: "123" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  expect(await screen.findByText(/for the author/)).toBeTruthy();
  expect(save).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("PR author"), {
    target: { value: "" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][0]).toMatchObject({
    type: "cron",
    action: {
      child_definition: { polling: { config: { pr_id: "123", author: "" } } },
    },
  });
});
it("starts a new cron waiting definition with blank PR ID and author", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const save = vi.fn(async (polling: Polling) => makeTask({ polling, version: 1 }));
  render(<SourceEditor busy={false} onSave={save} />);
  fireEvent.change(screen.getByLabelText("Polling type"), {
    target: { value: "cron" },
  });
  fireEvent.click(screen.getByLabelText("Waiting settings for new tasks"));
  expect((screen.getByLabelText("PR ID") as HTMLInputElement).value).toBe("");
  expect((screen.getByLabelText("PR author") as HTMLInputElement).value).toBe(
    "",
  );
});

it("edits initial launch policy independently of execution approvals", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const save = vi.fn(async (polling: Polling, _enabled: boolean, version: number) => makeTask({ polling, version: version + 1 }));
  render(<SourceEditor task={sourceTask()} busy={false} onSave={save} />);
  expect(
    (screen.getByLabelText("Initial launch") as HTMLSelectElement).value,
  ).toBe("manual");
  fireEvent.change(screen.getByLabelText("Initial launch"), {
    target: { value: "trusted_authors" },
  });
  fireEvent.change(screen.getByLabelText("Trusted PR authors"), {
    target: { value: "alice, bob" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][0]).toMatchObject({
    action: {
      initial_launch: { mode: "trusted_authors", authors: ["alice", "bob"] },
    },
  });
  expect(screen.getByText(/continuations require Start/)).toBeTruthy();
});

it("loads full details only on selection and retries failed details without blank editors", async () => {
  const original = vi.mocked(fetch).getMockImplementation()!;
  let failed = true;
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (path === "/v1/tasks/task-1" && failed) return { ok: false, status: 503, json: async () => ({ error: "Details unavailable" }) } as Response;
    return original(path, options);
  });
  render(<App />);
  const card = await screen.findByRole("button", { name: /CHAT Review the next revision/ });
  expect(calls.map((c) => c.path)).toEqual(["/v1/task-cards"]);
  fireEvent.click(card);
  await screen.findByText("Details unavailable");
  expect(screen.queryByRole("button", { name: "Save this task" })).toBeNull();
  failed = false;
  fireEvent.click(screen.getByRole("button", { name: "Retry details" }));
  await screen.findByRole("combobox", { name: "Move task" });
});
it("ignores a delayed response after selecting another compact card", async () => {
  rows.push(makeTask({ id: "task-2", title: "Other task" }));
  const original = vi.mocked(fetch).getMockImplementation()!;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>((done) => { resolve = done; });
  vi.mocked(fetch).mockImplementation((path, options) => path === "/v1/tasks/task-1" ? held : original(path, options));
  render(<App />);
  fireEvent.click(await screen.findByRole("button", { name: /CHAT Review the next revision/ }));
  await screen.findByText("Loading details…");
  fireEvent.click(screen.getByRole("button", { name: /CHAT Other task/ }));
  await screen.findByDisplayValue("Other task");
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
  await waitFor(() => expect((screen.getByLabelText("Title") as HTMLTextAreaElement).value).toBe("Other task"));
  expect(screen.queryByDisplayValue("Review the next revision")).toBeNull();
});
it("preserves an unsaved source prompt when background details refresh fails and retries", async () => {
  rows = [sourceTask()];
  window.history.replaceState(null, "", "/?task=source");
  render(<App />);
  const prompt = await screen.findByLabelText("Initial prompt");
  fireEvent.change(prompt, { target: { value: "Unsaved draft" } });
  const original = vi.mocked(fetch).getMockImplementation()!;
  let failed = true;
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (path === "/v1/tasks/source" && failed) return { ok: false, status: 503, json: async () => ({ error: "Details refresh unavailable" }) } as Response;
    return original(path, options);
  });
  fireEvent.focus(window);
  await screen.findByText("Details refresh unavailable");
  expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe("Unsaved draft");
  expect(screen.getByLabelText("Initial prompt")).toBe(prompt);
  failed = false;
  fireEvent.click(screen.getByRole("button", { name: "Retry details" }));
  await waitFor(() => expect(screen.queryByText("Details refresh unavailable")).toBeNull());
  expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe("Unsaved draft");
  expect(screen.getByLabelText("Initial prompt")).toBe(prompt);
});
it("does not promote stale full settings to the compact reorder version", async () => {
  rows = [makeTask({ config: { ...emptyConfig(), initial_prompt: "Old settings" } })];
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  await screen.findByRole("combobox", { name: "Move task" });
  rows = [makeTask({ version: 2, config: { ...emptyConfig(), initial_prompt: "External settings" } })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>((done) => { resolve = done; });
  let detailReads = 0;
  vi.mocked(fetch).mockImplementation((path, options) => {
    if (path === "/v1/tasks/task-1" && (!options?.method || options.method === "GET")) {
      detailReads++;
      if (detailReads <= 2) return held;
    }
    if (String(path).endsWith("/reorder")) return original(path, options).then(async (response) => ({ ...response, json: async () => compactTask(await response.json()) }) as Response);
    return original(path, options);
  });
  fireEvent.focus(window);
  await waitFor(() => expect(detailReads).toBe(2));
  const card = document.querySelector('[data-task-id="task-1"]')!;
  const dataTransfer = { setData() {}, getData: () => "task-1", effectAllowed: "", dropEffect: "" };
  fireEvent.dragStart(card, { dataTransfer });
  fireEvent.drop(screen.getByRole("region", { name: "TODO column" }), { dataTransfer, clientY: 0 });
  await waitFor(() => expect(rows[0].version).toBe(3));
  await waitFor(() => expect(detailReads).toBeGreaterThan(2));
  resolve({ ok: true, status: 200, json: async () => makeTask({ version: 2, config: { ...emptyConfig(), initial_prompt: "Delayed obsolete settings" } }) } as Response);
  fireEvent.click(screen.getByRole("tab", { name: "Polling" }));
  await waitFor(() => expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe("External settings"));
});

it("edits overview text preserving execution/source and exposes History", async () => {
  const config = { ...emptyConfig(), initial_prompt: "Keep prompt" };
  rows = [makeTask({ source_url: "https://example.com/pr", config, polling: { ...defaultPolling(), config: { ...defaultPolling().config, pr_id: "42", login: "reviewer" } } })];
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "New title" } });
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "First line\nSecond line" } });
  fireEvent.blur(screen.getByLabelText("Title"));
  await waitFor(() => expect(rows[0].title).toBe("New title"));
  expect(rows[0].description).toBe("First line\nSecond line");
  expect(rows[0].config).toEqual(config);
  expect(rows[0].source_url).toBe("https://example.com/pr");
  expect(screen.queryByRole("tab", { name: "Settings" })).toBeNull();
  expect(screen.queryByRole("tab", { name: "Activity" })).toBeNull();
  fireEvent.click(screen.getByRole("tab", { name: "History" }));
  await waitFor(() => expect(calls.some(c => c.path.endsWith("/events"))).toBe(true));
  fireEvent.click(screen.getByRole("tab", { name: "Polling" }));
  expect(screen.getByLabelText("Source URL")).toBeTruthy();
  expect(screen.getByLabelText("Initial prompt")).toBeTruthy();
  expect(screen.queryByLabelText("Title")).toBeNull();
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "123" } });
  fireEvent.change(screen.getByLabelText("Initial prompt"), { target: { value: "Changed prompt" } });
  await waitFor(() => expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest settings" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(rows[0].config.initial_prompt).toBe("Changed prompt"));
  expect(rows[0].title).toBe("New title");
  await waitFor(() => expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest settings" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(rows[0].polling?.config.pr_id).toBe("123"));
  fireEvent.change(screen.getByLabelText("Initial prompt"), { target: { value: "Another draft" } });
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "456" } });
  await waitFor(() => expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest settings" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(rows[0].polling?.config.pr_id).toBe("456"));
  expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe("Another draft");
  await waitFor(() => expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest settings" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(rows[0].config.initial_prompt).toBe("Another draft"));
});
it("preserves overview drafts and blocks replacement of externally edited fields", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Local draft" } });
  rows = [makeTask({ version: 2, source_url: "https://example.com/new-source", updated_at: "2026-01-02T12:00:00Z", config: { ...emptyConfig(), initial_prompt: "External prompt" } })];
  fireEvent.focus(window);
  await waitFor(() => expect(calls.filter(c => c.path === "/v1/tasks/task-1").length).toBeGreaterThan(1));
  await waitFor(() => expect(screen.queryByText("Loading details…")).toBeNull());
  await screen.findByRole("link", { name: /Open source/ });
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Local draft");
  fireEvent.blur(screen.getByLabelText("Title"));
  await waitFor(() => expect(rows[0].title).toBe("Local draft"));
  expect(rows[0].config.initial_prompt).toBe("External prompt");
  await waitFor(() => expect(screen.queryByText("Saving overview…")).toBeNull());
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "My description" } });
  rows = [{ ...rows[0], version: 4, description: "External description" }];
  fireEvent.focus(window);
  await screen.findByRole("button", { name: "Reload latest overview" });
  expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("My description");
  const writes = calls.filter(c => c.method === "PATCH").length;
  fireEvent.blur(screen.getByLabelText("Title"));
  expect(calls.filter(c => c.method === "PATCH").length).toBe(writes);
  fireEvent.click(screen.getByRole("button", { name: "Reload latest overview" }));
  expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("External description");
});
it("does not bless a concurrent overview save reply and preserves typing during a save", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>(done => { resolve = done; });
  vi.mocked(fetch).mockImplementation((path, options) => options?.method === "PATCH" ? held : original(path, options));
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Submitted title" } });
  fireEvent.blur(screen.getByLabelText("Title"));
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Typing while saving" } });
  rows = [makeTask({ title: "Concurrent title", version: 3 })];
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
  await screen.findByRole("button", { name: "Reload latest overview" });
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Typing while saving");
  await waitFor(() => expect(screen.queryByText("Saving overview…")).toBeNull());
  fireEvent.blur(screen.getByLabelText("Title"));
  expect(rows[0].title).toBe("Concurrent title");
});

it("does not bless a concurrent unified workflow save reply", async () => {
  const initial = { ...defaultPolling(), config: { ...defaultPolling().config, pr_id: "42" } };
  const t = makeTask({ polling: initial });
  const concurrent = { ...t, version: 3, polling: { ...initial, config: { ...initial.config, pr_id: "44" } } };
  const save = vi.fn(async () => concurrent);
  const { rerender } = render(<WorkflowForm task={t} busy={false} onSave={save} onActivate={vi.fn()} onLink={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "43" } });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  rerender(<WorkflowForm task={concurrent} busy={false} onSave={save} onActivate={vi.fn()} onLink={vi.fn()} />);
  await screen.findByRole("button", { name: "Reload latest settings" });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(save).toHaveBeenCalledTimes(1);
  expect((screen.getByLabelText("PR ID") as HTMLInputElement).value).toBe("43");
});
it("saves priority with Cmd+Enter and links an existing chat from Overview", async () => {
  rows = [makeTask({ thread_id: "", priority: "normal" })];
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "Keyboard saved" } });
  fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "high" } });
  fireEvent.keyDown(title, { key: "Enter", metaKey: true, isComposing: true });
  fireEvent.keyDown(title, { key: "Enter", metaKey: true, repeat: true });
  expect(calls.filter(c => c.method === "PATCH")).toHaveLength(0);
  fireEvent.keyDown(title, { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].priority).toBe("high"));
  expect(rows[0].title).toBe("Keyboard saved");
  expect((screen.getByLabelText("Priority for Keyboard saved") as HTMLSelectElement).value).toBe("high");
  await waitFor(() => expect((screen.getByRole("button", { name: "Link chat" }) as HTMLButtonElement).disabled).toBe(false));
  const chat = screen.getByLabelText("Codex chat");
  fireEvent.change(chat, { target: { value: "codex://threads/existing-chat" } });
  fireEvent.keyDown(chat, { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].thread_id).toBe("existing-chat"));
  expect(calls.find(c => c.path.endsWith("/chat") && c.method === "POST")?.body).toEqual({ expected_version: 2, thread_id: "existing-chat" });
  const links = screen.getAllByRole("link", { name: /Open in Codex/ });
  expect(links).toHaveLength(2);
  expect(links.every(link => link.getAttribute("href") === "codex://threads/existing-chat")).toBe(true);
  const boardLink = links.find(link => link.closest(".card-slot"))!;
  expect(boardLink.closest("button")).toBeNull();
  const count = calls.filter(c => c.method !== "GET").length;
  boardLink.addEventListener("click", e => e.preventDefault());
  fireEvent.click(boardLink);
  expect(calls.filter(c => c.method !== "GET")).toHaveLength(count);
});
it("Cmd+Enter saves the focused polling section and respects native validation", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  const title = await screen.findByLabelText("Title");
  fireEvent.change(title, { target: { value: "" } });
  fireEvent.keyDown(title, { key: "Enter", metaKey: true });
  expect(calls.filter(c => c.method === "PATCH")).toHaveLength(0);
  fireEvent.click(screen.getByRole("tab", { name: "Polling" }));
  const prompt = screen.getByLabelText("Initial prompt");
  fireEvent.change(prompt, { target: { value: "Keyboard prompt" } });
  fireEvent.keyDown(prompt, { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].config.initial_prompt).toBe("Keyboard prompt"));
  expect(calls.filter(c => c.method === "PUT")).toHaveLength(0);
  await waitFor(() => expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Configure waiting" }));
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "43" } });
  expect(screen.queryByLabelText("Your Arcanum login")).toBeNull();
  fireEvent.click(screen.getByLabelText("Move to Done when PR is merged"));
  fireEvent.keyDown(screen.getByLabelText("PR ID"), { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].polling?.config.pr_id).toBe("43"));
  expect(rows[0].polling?.config.complete_on_merge).toBe(true);
  await waitFor(() => expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByLabelText("Move to Done when PR is merged"));
  fireEvent.keyDown(screen.getByLabelText("PR ID"), { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].polling?.config.complete_on_merge).toBe(false));
});
it("preserves new overview typing during an ordinary save and saves sources with Cmd+Enter", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>(done => { resolve = done; });
  vi.mocked(fetch).mockImplementation((path, options) => options?.method === "PATCH" ? held : original(path, options));
  const view = render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Submitted title" } });
  fireEvent.blur(screen.getByLabelText("Title"));
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "New draft" } });
  rows = [makeTask({ title: "Submitted title", version: 2 })];
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
  await screen.findByText("Overview saved.");
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("New draft");
  expect(screen.queryByRole("button", { name: "Reload latest overview" })).toBeNull();
  view.unmount();
  rows = [sourceTask()];
  vi.mocked(fetch).mockImplementation(original);
  window.history.replaceState(null, "", "/?task=source");
  render(<App />);
  const prompt = await screen.findByLabelText("Initial prompt");
  fireEvent.change(prompt, { target: { value: "Keyboard source prompt" } });
  fireEvent.keyDown(prompt, { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].polling?.action.child_definition?.config.initial_prompt).toBe("Keyboard source prompt"));
});
it("edits card priority without details or ordering and attaches a chat inline", async () => {
  rows = [makeTask({ thread_id: "" })];
  render(<App />);
  const priority = await screen.findByLabelText("Priority for Review the next revision");
  fireEvent.change(priority, { target: { value: "high" } });
  await waitFor(() => expect(rows[0].priority).toBe("high"));
  expect(calls.find(c => c.path.endsWith("/priority") && c.method === "POST")?.body).toEqual({ expected_version: 1, priority: "high" });
  expect(calls.some(c => c.path === "/v1/tasks/task-1")).toBe(false);
  expect(rows[0].position).toBe(1);
  expect(screen.queryByLabelText("Title")).toBeNull();
  const codex = screen.getByRole("button", { name: "Link Codex: no chat linked" });
  expect(codex.closest(".task-card")).toBeNull();
  await waitFor(() => expect((codex as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(codex);
  const cancelledChat = screen.getByLabelText("Codex chat for Review the next revision");
  fireEvent.change(cancelledChat, { target: { value: "Discard this draft" } });
  const beforeCancel = calls.filter(call => call.method !== "GET").length;
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByLabelText("Codex chat for Review the next revision")).toBeNull();
  expect(calls.filter(call => call.method !== "GET")).toHaveLength(beforeCancel);
  fireEvent.click(codex);
  const chat = screen.getByLabelText("Codex chat for Review the next revision");
  expect((chat as HTMLInputElement).value).toBe("");
  fireEvent.change(chat, { target: { value: "codex://threads/inline-chat" } });
  fireEvent.click(screen.getByRole("button", { name: "Link chat" }));
  await waitFor(() => expect(rows[0].thread_id).toBe("inline-chat"));
  expect(screen.getByRole("link", { name: /Open in Codex/ }).getAttribute("href")).toBe("codex://threads/inline-chat");
  expect(screen.queryByLabelText("Title")).toBeNull();
});
it("keeps an Overview text draft after a safe compact card priority change", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Unsaved text" } });
  fireEvent.change(screen.getByLabelText("Priority for Review the next revision"), { target: { value: "low" } });
  await waitFor(() => expect(rows[0].priority).toBe("low"));
  await waitFor(() => expect(screen.queryByText("Saving overview…")).toBeNull());
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest overview" })).toBeNull());
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Unsaved text");
  fireEvent.blur(screen.getByLabelText("Title"));
  await waitFor(() => expect(rows[0].title).toBe("Unsaved text"));
  expect(rows[0].priority).toBe("low");
});
it("retains drafts on inline priority conflict and does not certify unseen settings from a newer compact reply", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let rejectPriority = true;
  let holdDetails = false;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>(done => { resolve = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (options?.method === "POST" && String(path).endsWith("/priority")) {
      if (rejectPriority) return { ok: false, status: 409, json: async () => ({ error: "Priority changed elsewhere" }) } as Response;
      rows = [makeTask({ priority: "high", version: 3, config: { ...emptyConfig(), initial_prompt: "Unseen newer prompt" } })];
      holdDetails = true;
      return { ok: true, status: 200, json: async () => compactTask(rows[0]) } as Response;
    }
    if (holdDetails && path === "/v1/tasks/task-1" && options?.method === "PATCH") {
      calls.push({ path, method: "PATCH", body: JSON.parse(options.body as string) });
      return { ok: false, status: 409, json: async () => ({ error: "Stale detail version" }) } as Response;
    }
    if (holdDetails && path === "/v1/tasks/task-1" && (!options?.method || options.method === "GET")) return held;
    return original(path, options);
  });
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Keep draft" } });
  fireEvent.change(screen.getByLabelText("Priority for Review the next revision"), { target: { value: "high" } });
  await screen.findByText("Priority changed elsewhere");
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Keep draft");
  await waitFor(() => expect(screen.queryByText("Saving overview…")).toBeNull());
  rejectPriority = false;
  fireEvent.change(screen.getByLabelText("Priority for Review the next revision"), { target: { value: "high" } });
  await waitFor(() => expect(screen.queryByText("Saving overview…")).toBeNull());
  fireEvent.blur(screen.getByLabelText("Title"));
  await waitFor(() => expect(calls.some(call => call.method === "PATCH")).toBe(true));
  expect(calls.find(call => call.method === "PATCH")?.body).toMatchObject({ expected_version: 1 });
  // The compact reply must not grant stale full settings its newer version.
  rows = [makeTask({ priority: "high", version: 3, config: { ...emptyConfig(), initial_prompt: "Unseen newer prompt" } })];
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
});

it("adds, opens, edits and removes a ticket without navigating or changing task settings", async () => {
  rows = [makeTask({ source_url: "https://example.com/pr", config: { ...emptyConfig(), initial_prompt: "Keep prompt" } })];
  render(<App />);
  const add = await screen.findByRole("button", { name: "Add ticket" });
  expect(add.closest(".task-card")).toBeNull();
  fireEvent.click(add);
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "https://tracker.example/TASK-123" } });
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(calls.some(c => c.path.endsWith("/ticket"))).toBe(false);
  fireEvent.click(add);
  const input = screen.getByLabelText("Ticket URL");
  fireEvent.change(input, { target: { value: "https://tracker.example/TASK-123" } });
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  const ticket = await screen.findByRole("link", { name: "TASK-123" });
  expect(ticket.getAttribute("href")).toBe("https://tracker.example/TASK-123");
  expect(ticket.getAttribute("title")).toBe("https://tracker.example/TASK-123");
  expect(ticket.getAttribute("target")).toBe("_blank");
  expect(ticket.getAttribute("rel")).toContain("noreferrer");
  expect(ticket.getAttribute("draggable")).toBe("false");
  expect(calls.find(c => c.path.endsWith("/ticket"))?.body).toEqual({ expected_version: 1, ticket_url: "https://tracker.example/TASK-123" });
  expect(rows[0].source_url).toBe("https://example.com/pr");
  expect(rows[0].config.initial_prompt).toBe("Keep prompt");
  expect(rows[0].position).toBe(1);
  expect(screen.queryByLabelText("Title")).toBeNull();
  await waitFor(() => expect((screen.getByRole("button", { name: "Edit ticket" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Edit ticket" }));
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "" } });
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  await screen.findByRole("button", { name: "Add ticket" });
  expect(rows[0].ticket_url).toBe("");
});
it("saves an Overview ticket with Cmd+Enter while keeping its title draft", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Draft title" } });
  const detail = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  fireEvent.click(detail.getByRole("button", { name: "Add ticket" }));
  const input = detail.getByLabelText("Ticket URL");
  fireEvent.change(input, { target: { value: " task-123 " } });
  fireEvent.keyDown(input, { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].ticket_url).toBe("https://st.yandex-team.ru/TASK-123"));
  expect((detail.getByLabelText("Title") as HTMLInputElement).value).toBe("Draft title");
  await waitFor(() => expect(detail.queryByText("Saving overview…")).toBeNull());
  await waitFor(() => expect(detail.queryByRole("button", { name: "Reload latest overview" })).toBeNull());
  fireEvent.blur(detail.getByLabelText("Title"));
  await waitFor(() => expect(rows[0].title).toBe("Draft title"));
  expect(rows[0].ticket_url).toBe("https://st.yandex-team.ru/TASK-123");
});
it("preserves ticket drafts across unrelated versions and fences external ticket edits", async () => {
  const original = makeTask({ ticket_url: "https://tracker.example/TASK-123" });
  const save = vi.fn(async (_url: string, version: number) => compactTask({ ...original, version: version + 1 }));
  const view = render(<TicketControl task={original} busy={false} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Edit ticket" }));
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "https://tracker.example/TASK-456" } });
  view.rerender(<TicketControl task={{ ...original, version: 2, title: "Updated title" }} busy={false} onSave={save} />);
  expect((screen.getByLabelText("Ticket URL") as HTMLInputElement).value).toBe("https://tracker.example/TASK-456");
  view.rerender(<TicketControl task={{ ...original, version: 3, ticket_url: "https://tracker.example/TASK-789" }} busy={false} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  expect(save).not.toHaveBeenCalled();
  expect((screen.getByLabelText("Ticket URL") as HTMLInputElement).value).toBe("https://tracker.example/TASK-456");
  fireEvent.click(screen.getByRole("button", { name: "Reload latest ticket" }));
  expect((screen.getByLabelText("Ticket URL") as HTMLInputElement).value).toBe("https://tracker.example/TASK-789");
});
it("does not grant a concurrent ticket save reply to the submitted draft", async () => {
  const original = makeTask({ ticket_url: "https://tracker.example/TASK-123" });
  const concurrent = compactTask({ ...original, ticket_url: "https://tracker.example/TASK-789", version: 3 });
  const save = vi.fn(async () => concurrent);
  const view = render(<TicketControl task={original} busy={false} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Edit ticket" }));
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "https://tracker.example/TASK-456" } });
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  await screen.findByText(/ticket changed after saving/);
  view.rerender(<TicketControl task={concurrent} busy={false} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  expect(save).toHaveBeenCalledTimes(1);
  expect((screen.getByLabelText("Ticket URL") as HTMLInputElement).value).toBe("https://tracker.example/TASK-456");
});
it("retains ticket typing during pending saves and rejects non HTTP URLs", async () => {
  const original = makeTask();
  let resolve!: (value: ReturnType<typeof compactTask>) => void;
  const held = new Promise<ReturnType<typeof compactTask>>(done => { resolve = done; });
  const save = vi.fn(async () => held);
  const view = render(<TicketControl task={original} busy={false} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Add ticket" }));
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "javascript:alert(1)" } });
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  expect(save).not.toHaveBeenCalled();
  for (const url of ["https://user:password@tracker.example/TASK-123", "https:tracker.example/TASK-123", "//tracker.example/TASK-123"]) {
    fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: url } });
    fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
    expect(save).not.toHaveBeenCalled();
  }
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "https://tracker.example/TASK-123" } });
  fireEvent.click(screen.getByRole("button", { name: "Save ticket" }));
  fireEvent.change(screen.getByLabelText("Ticket URL"), { target: { value: "https://tracker.example/TASK-456" } });
  const saved = compactTask({ ...original, ticket_url: "https://tracker.example/TASK-123", version: 2 });
  resolve(saved);
  view.rerender(<TicketControl task={saved} busy={false} onSave={save} />);
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest ticket" })).toBeNull());
  expect((screen.getByLabelText("Ticket URL") as HTMLInputElement).value).toBe("https://tracker.example/TASK-456");
});
it("does not advance unseen full settings from a newer compact ticket reply", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let waiting = false;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>(done => { resolve = done; });
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (String(path).endsWith("/ticket") && options?.method === "POST") {
      rows = [makeTask({ version: 3, source_url: "https://example.com/new-source", ticket_url: "https://tracker.example/TASK-123", updated_at: "2026-01-02T12:00:00Z", config: { ...emptyConfig(), initial_prompt: "External prompt" } })];
      waiting = true;
      return { ok: true, status: 200, json: async () => compactTask(rows[0]) } as Response;
    }
    if (waiting && path === "/v1/tasks/task-1") {
      if (!options?.method || options.method === "GET") return held;
      if (options.method === "PATCH") {
        calls.push({ path, method: "PATCH", body: JSON.parse(options.body as string) });
        return { ok: false, status: 409, json: async () => ({ error: "Stale detail version" }) } as Response;
      }
    }
    return original(path, options);
  });
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Keep title draft" } });
  const detail = within(screen.getByRole("complementary", { name: "Task details and settings" }));
  fireEvent.click(detail.getByRole("button", { name: "Add ticket" }));
  fireEvent.change(detail.getByLabelText("Ticket URL"), { target: { value: "https://tracker.example/TASK-123" } });
  fireEvent.click(detail.getByRole("button", { name: "Save ticket" }));
  await screen.findByRole("link", { name: "TASK-123" });
  await waitFor(() => expect(detail.queryByText("Saving overview…")).toBeNull());
  fireEvent.blur(detail.getByLabelText("Title"));
  await detail.findByText("Stale detail version");
  expect(calls.find(c => c.method === "PATCH")?.body).toMatchObject({ expected_version: 1 });
  waiting = false;
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
  await detail.findByRole("link", { name: /Open source/ });
  await waitFor(() => expect(detail.queryByText("Saving overview…")).toBeNull());
  await waitFor(() => expect(detail.queryByRole("button", { name: "Reload latest overview" })).toBeNull());
  fireEvent.click(detail.getByRole("button", { name: "Retry overview save" }));
  await waitFor(() => expect(rows[0].title).toBe("Keep title draft"));
  expect(rows[0].config.initial_prompt).toBe("External prompt");
  expect(rows[0].ticket_url).toBe("https://tracker.example/TASK-123");
});

it("saves merge completion independently of ANY conditions and preserves a conflicting draft", async () => {
  const initial = defaultPolling(); initial.config.pr_id = "123";
  const t = makeTask({ polling: initial });
  const save = vi.fn(async (input) => ({ ...t, ...input, version: 2 }));
  const view = render(<WorkflowForm task={t} busy={false} onSave={save} onActivate={vi.fn()} onLink={vi.fn()} />);
  expect(screen.queryByLabelText("Your Arcanum login")).toBeNull();
  const merged = screen.getByLabelText("Move to Done when PR is merged") as HTMLInputElement;
  expect(merged.checked).toBe(false);
  fireEvent.click(merged);
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][0].polling.config).toMatchObject({ complete_on_merge: true, conditions: ["new_commit", "all_answered"] });
  fireEvent.click(merged);
  view.rerender(<WorkflowForm task={{ ...t, version: 3, polling: { ...initial, config: { ...initial.config, pr_id: "456", complete_on_merge: true } } }} busy={false} onSave={save} onActivate={vi.fn()} onLink={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(await screen.findByRole("button", { name: "Reload latest settings" })).toBeTruthy();
  expect(merged.checked).toBe(false);
  expect(save).toHaveBeenCalledTimes(1);
});

it.each(["pr_review_source", "pr_changes_source", "cron"])("saves child merge completion both ways for %s", async type => {
  const { SourceEditor } = await import("./SourceEditor");
  const task = sourceTask();
  task.polling = defaultPolling(type);
  task.polling.action.child_definition = { title: "Child", description: "", config: { ...emptyConfig(), initial_prompt: "Work", continuation_prompt: "Follow up" } };
  const save = vi.fn(async (polling: Polling, _enabled: boolean, version: number) => makeTask({ polling, version: version + 1 }));
  render(<SourceEditor task={task} busy={false} onSave={save} />);
  expect(screen.queryByLabelText("Your Arcanum login")).toBeNull();
  expect(screen.queryByLabelText("Move to Done when PR is merged")).toBeNull();
  fireEvent.click(screen.getByLabelText("Waiting settings for new tasks"));
  const merged = screen.getByLabelText("Move to Done when PR is merged") as HTMLInputElement;
  const expected = type !== "cron";
  expect(merged.checked).toBe(expected);
  if (type === "cron") fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "123" } });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][0].action.child_definition!.polling!.config.complete_on_merge).toBe(expected);
  fireEvent.click(merged);
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][0].action.child_definition!.polling!.config.complete_on_merge).toBe(!expected);
  expect(save.mock.calls[1][0].config).not.toHaveProperty("complete_on_merge");
});
it('fences source drafts after a newer concurrent save reply and reloads authoritative settings', async () => {
  const { SourceEditor } = await import('./SourceEditor');
  const original = sourceTask();
  const concurrent = structuredClone(original); concurrent.version += 2;
  concurrent.polling!.action.child_definition!.config.initial_prompt = 'External instruction';
  const save = vi.fn(async () => concurrent);
  const view = render(<SourceEditor task={original} busy={false} onSave={save} />);
  fireEvent.change(screen.getByLabelText('Initial prompt'), { target: { value: 'Local instruction' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  view.rerender(<SourceEditor task={concurrent} busy={false} onSave={save} />);
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  expect(save).toHaveBeenCalledOnce();
  expect((screen.getByLabelText('Initial prompt') as HTMLTextAreaElement).value).toBe('Local instruction');
  fireEvent.click(screen.getByRole('button', { name: 'Reload latest source' }));
  expect((screen.getByLabelText('Initial prompt') as HTMLTextAreaElement).value).toBe('External instruction');
});
it('copies PR feedback waiting defaults with a readonly detected PR placeholder', async () => {
  const { SourceEditor } = await import('./SourceEditor');
  const t = sourceTask(); t.polling!.type = 'pr_changes_source';
  const save = vi.fn(async (polling: Polling, _enabled: boolean, version: number) => makeTask({ polling, version: version + 1 }));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  fireEvent.click(screen.getByLabelText('Waiting settings for new tasks'));
  fireEvent.change(screen.getByLabelText('Continuation prompt'), { target: { value: 'Follow up' } });
  const id = screen.getByLabelText('PR ID') as HTMLInputElement;
  expect(id.value).toBe('{{pr_id}}'); expect(id.readOnly).toBe(true);
  expect(screen.getByText('From detected PR')).toBeTruthy();
  expect(screen.queryByRole('link', { name: /Open PR/ })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect((save.mock.calls[0] as unknown as [Polling])[0].action.child_definition!.polling!.config.conditions).toEqual(['all_resolved', 'request_changes']);
});
it('adopts normalized source settings while retaining fields typed during the save', async () => {
  const { SourceEditor } = await import('./SourceEditor');
  const original = sourceTask(); let resolve!: (task: Task) => void;
  const held = new Promise<Task>(done => { resolve = done; });
  const save = vi.fn(async (_polling: Polling) => held);
  render(<SourceEditor task={original} busy={false} onSave={save} />);
  fireEvent.change(screen.getByLabelText('Initial prompt'), { target: { value: 'Submitted prompt' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  fireEvent.change(screen.getByLabelText('Initial prompt'), { target: { value: 'Typed while saving' } });
  fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'new-model' } });
  const saved = { ...original, version: original.version + 1, polling: structuredClone(save.mock.calls[0][0]) };
  saved.polling!.action.child_definition!.config.execution.cwd = '/workspace/project';
  saved.polling!.action.child_definition!.config.execution.model = 'daemon-default';
  resolve(saved);
  await waitFor(() => expect((screen.getByLabelText('Working directory') as HTMLInputElement).value).toBe('/workspace/project'));
  expect((screen.getByLabelText('Initial prompt') as HTMLTextAreaElement).value).toBe('Typed while saving');
  expect((screen.getByLabelText('Model') as HTMLInputElement).value).toBe('new-model');
  save.mockImplementationOnce(async polling => ({ ...saved, version: saved.version + 1, polling }));
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][0].action.child_definition!.config.execution.cwd).toBe('/workspace/project');
});
