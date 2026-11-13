// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App, TaskCard } from "./App";
import { TaskRelations } from "./TaskRelations";
import { PRControl } from "./PRControl";
import { compactTask, emptyConfig, type Task } from "./model";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const task = (id: string, extra: Partial<Task> = {}): Task => ({
  id, title: id, description: "Description", card_type: "task", column: "todo", position: 0,
  thread_id: "", source_url: "", source_key: "", config: emptyConfig(), version: 1,
  created_at: "", updated_at: "", requests: [], ...extra,
});

it("keeps Task type with a chat and shows linked PRs as independent one-line selections", () => {
  const parent = compactTask(task("Development", { thread_id: "dev-chat", ticket_url: "https://st.yandex-team.ru/TEAM-42" }));
  const pr = compactTask(task("Fixes", { card_type: "pr", arcanum_pr_id: "42", source_url: "https://a.yandex-team.ru/review/42", parent_task_id: parent.id, column: "polling" }));
  const selectParent = vi.fn(), selectPR = vi.fn();
  render(<TaskCard task={parent} selected={false} onSelect={selectParent} onTicket={vi.fn()} linkedPRs={[pr]} onSelectPR={selectPR} />);
  expect(screen.getByText("TASK")).toBeTruthy();
  expect(screen.queryByText("Add PR")).toBeNull();
  const row = screen.getByRole("button", { name: "Open PR #42 · Polling" });
  expect(row.textContent).toBe("#42Polling");
  expect(within(row).queryByText("Fixes")).toBeNull();
  fireEvent.click(row);
  expect(selectPR).toHaveBeenCalledWith(pr);
  expect(selectParent).not.toHaveBeenCalled();
});

it("shows Arc status next to the full PR footer link without Tracker", () => {
  const pr = compactTask(task("Fixes", { card_type: "pr", arcanum_pr_id: "42", source_url: "https://a.yandex-team.ru/review/42", pr_status: { label: "Waiting for changes", lifecycle: "open", common_status: "waiting_for_changes", stale: true, unavailable: false, last_attempt_at: "", last_success_at: "" } }));
  const { container } = render(<TaskCard task={pr} selected={false} onSelect={vi.fn()} onTicket={vi.fn()} />);
  expect(screen.getByText("PR")).toBeTruthy();
  expect(screen.queryByText("Add ticket")).toBeNull();
  const footer = container.querySelector(".card-controls")!;
  expect(within(footer as HTMLElement).getByText("Waiting for changes")).toBeTruthy();
  expect(within(footer as HTMLElement).getByRole("link", { name: "Arcanum PR #42" })).toBeTruthy();
  expect(screen.getByLabelText("Arc status is stale")).toBeTruthy();
});

it("keeps PR rows visible under a searched Task and navigates between the two chats", async () => {
  const parent = task("development", { title: "Development", column: "review", thread_id: "development-chat", ticket_url: "https://st.yandex-team.ru/TEAM-42" });
  const pr = task("fix-review", { title: "Fix review", card_type: "pr", arcanum_pr_id: "42", source_url: "https://a.yandex-team.ru/review/42", thread_id: "review-chat", parent_task_id: parent.id, column: "inbox" });
  let rows = [parent, pr];
  window.history.replaceState(null, "", "/"); window.localStorage.clear();
  vi.stubGlobal("EventSource", class { onmessage = null; onopen = null; onerror = null; addEventListener() {} close() {} });
  vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
    const path = String(input); let result: unknown = [];
    if (path === "/v1/task-cards") result = rows.map(row => compactTask(row));
    else if (path.endsWith("/move")) {
      const body = JSON.parse(String(init?.body));
      rows = rows.map(row => path.includes(encodeURIComponent(row.id)) ? { ...row, column: body.column, version: row.version + 1 } : row);
      result = rows.find(row => path.includes(encodeURIComponent(row.id)));
    } else if (!path.endsWith("/events")) result = rows.find(row => path === `/v1/tasks/${row.id}`);
    return { ok: true, status: 200, json: async () => result };
  }));
  render(<App />);
  await screen.findByRole("button", { name: "Open PR #42 · Inbox" });
  for (const query of ["42", "https://a.yandex-team.ru/review/42", "https://arcanum.yandex-team.ru/review/42/details"]) {
    fireEvent.change(screen.getByPlaceholderText(/Find a task/), { target: { value: query } });
    expect(screen.getByRole("button", { name: /PR Fix review/ })).toBeTruthy();
  }
  fireEvent.change(screen.getByPlaceholderText(/Find a task/), { target: { value: "Development" } });
  expect(screen.queryByRole("button", { name: /PR Fix review/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Open PR #42 · Inbox" }));
  await waitFor(() => expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Fix review"));
  expect(screen.getByLabelText("Open development chat").getAttribute("href")).toContain("development-chat");
  fireEvent.click(screen.getByRole("combobox", { name: "Move task" }));
  expect(screen.queryByRole("option", { name: "Review" })).toBeNull();
  fireEvent.click(screen.getByRole("option", { name: "TODO" }));
  await screen.findByRole("button", { name: "Open PR #42 · TODO" });
  expect(rows[0].column).toBe("review");
  expect(rows[1].thread_id).toBe("review-chat");
  fireEvent.click(screen.getByRole("button", { name: "Development" }));
  await waitFor(() => expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Development"));
  expect(rows[0].thread_id).toBe("development-chat");
});

it("accepts another supported PR URL format without reporting a false save conflict", async () => {
  const pr = task("PR", { card_type: "pr", arcanum_pr_id: "42", source_url: "https://a.yandex-team.ru/review/42" });
  const save = vi.fn(async (url: string) => ({ ...pr, source_url: url, version: 2 }));
  render(<PRControl task={compactTask(pr)} onSave={save} busy={false} />);
  fireEvent.click(screen.getByRole("button", { name: "Edit PR" }));
  fireEvent.change(screen.getByLabelText("PR ID or URL"), { target: { value: "https://arcanum.yandex-team.ru/review/42/details" } });
  fireEvent.click(screen.getByRole("button", { name: "Save PR" }));
  await waitFor(() => expect(screen.queryByLabelText("PR ID or URL")).toBeNull());
  expect(save).toHaveBeenCalledWith("https://a.yandex-team.ru/review/42", 1);
  expect(screen.queryByRole("alert")).toBeNull();
});

it("preserves a relationship draft on conflict and requires explicit reload of its version", async () => {
  const parent = compactTask(task("Development"));
  const pr = task("Fixes", { card_type: "pr", arcanum_pr_id: "42", source_url: "https://a.yandex-team.ru/review/42" });
  const save = vi.fn().mockRejectedValueOnce(new Error("This card changed")).mockResolvedValue(undefined);
  const { rerender } = render(<TaskRelations task={pr} cards={[parent, compactTask(pr)]} busy={false} onSelect={vi.fn()} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Link Task" }));
  fireEvent.click(screen.getByRole("combobox", { name: "Parent Task" }));
  fireEvent.click(screen.getByRole("option", { name: "Development" }));
  fireEvent.click(screen.getByRole("button", { name: "Save Task link" }));
  await screen.findByRole("alert");
  expect(screen.getByRole("combobox", { name: "Parent Task" }).textContent).toContain("Development");
  const newer = { ...pr, version: 2 };
  rerender(<TaskRelations task={newer} cards={[parent, compactTask(newer)]} busy={false} onSelect={vi.fn()} onSave={save} />);
  fireEvent.click(screen.getByRole("button", { name: "Reload latest link" }));
  fireEvent.click(screen.getByRole("button", { name: "Save Task link" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][0].version).toBe(2);
  expect(save.mock.calls[1][1]).toBe(parent.id);
});

it("filters all columns by card type together with search while keeping linked PR navigation and drafts", async () => {
  const rows = [
    task("development", { title: "Development", column: "review" }),
    task("review-fixes", { title: "Review fixes", card_type: "pr", arcanum_pr_id: "42", source_url: "https://a.yandex-team.ru/review/42", parent_task_id: "development", column: "inbox" }),
    task("reading", { title: "Morning reading", card_type: "schedule", column: "polling" }),
    task("done-task", { title: "Finished development", column: "done", done_at: "2026-01-02T12:00:00Z" }),
    task("done-pr", { title: "Finished review", card_type: "pr", arcanum_pr_id: "43", source_url: "https://a.yandex-team.ru/review/43", column: "done", done_at: "2026-01-02T12:00:00Z" }),
    task("legacy", { title: "Legacy task", card_type: undefined }),
    task("source", { title: "Review source", card_type: "", polling: { type: "arcanum_review", mode: "repeat", config: {}, action: { type: "create_task" } } }),
  ];
  window.history.replaceState(null, "", "/"); window.localStorage.clear();
  vi.stubGlobal("EventSource", class { onopen = null; onerror = null; addEventListener() {} close() {} });
  const fetchMock = vi.fn(async (input: string, _init?: RequestInit) => {
    const path = String(input);
    const result = path === "/v1/task-cards?column=done" ? rows.filter(row=>row.column==="done").map(row=>compactTask(row)) : path === "/v1/task-cards" ? rows.filter(row=>row.column!=="done").map(row => compactTask(row)) : path.endsWith("/events") ? [] : rows.find(row => path === `/v1/tasks/${row.id}`);
    return { ok: true, status: path === "/v1/board-presence" ? 204 : 200, json: async () => result };
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<App />);
  await screen.findByRole("button", { name: /TASK Development/ });
  const choose = (label: string) => {
    const trigger = screen.getByRole("combobox", { name: "Card type filter" });
    fireEvent(trigger, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    fireEvent.click(trigger);
    const option = screen.getByRole("option", { name: label });
    fireEvent(option, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    fireEvent.click(option);
  };
  fireEvent.click(screen.getByRole("button", { name: "Show Done" }));
  await screen.findByRole("button",{name:/TASK Finished development/});
  choose("Task");
  expect(screen.getByRole("button", { name: /TASK Development/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /TASK Legacy task/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /TASK Finished development/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /PR Review fixes/ })).toBeNull();
  expect(screen.queryByRole("button", { name: /PR Finished review/ })).toBeNull();
  expect(screen.queryByRole("button", { name: /SCHEDULE Morning reading/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Open PR #42 · Inbox" }));
  const title = await screen.findByLabelText("Title");
  expect((title as HTMLInputElement).value).toBe("Review fixes");
  fireEvent.change(title, { target: { value: "Unsaved review notes" } });
  choose("PR");
  expect(new URLSearchParams(window.location.search).get("task")).toBe("review-fixes");
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Unsaved review notes");
  expect(screen.getByRole("button", { name: /PR Finished review/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /TASK Development/ })).toBeNull();
  fireEvent.change(screen.getByRole("textbox", { name: "Search tasks" }), { target: { value: "43" } });
  expect(screen.queryByRole("button", { name: /PR Review fixes/ })).toBeNull();
  expect(screen.getByRole("button", { name: /PR Finished review/ })).toBeTruthy();
  choose("Task");
  expect(screen.queryByRole("button", { name: /TASK Finished development/ })).toBeNull();
  fireEvent.change(screen.getByRole("textbox", { name: "Search tasks" }), { target: { value: "" } });
  choose("Schedule");
  expect(screen.getByRole("button", { name: /SCHEDULE Morning reading/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /TASK Development/ })).toBeNull();
  choose("All types");
  expect(screen.getByRole("button", { name: /TASK Development/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /PR Review fixes/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /Review source/ })).toBeTruthy();
  const card = screen.getByRole("button", { name: /TASK Development/ });
  fireEvent.dragStart(card, { dataTransfer: { setData: vi.fn(), effectAllowed: "" } });
  expect((screen.getByRole("combobox", { name: "Card type filter" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.dragEnd(card);
  expect((screen.getByRole("combobox", { name: "Card type filter" }) as HTMLButtonElement).disabled).toBe(false);
  const taskWrites = fetchMock.mock.calls.filter(([path, ...rest]) => String(path).startsWith("/v1/tasks") && (rest[0] as RequestInit | undefined)?.method && (rest[0] as RequestInit).method !== "GET");
  expect(taskWrites).toHaveLength(0);
});
