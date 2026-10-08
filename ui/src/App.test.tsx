// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { App } from "./App";
import { PollingForm } from "./forms";
import { compactTask, defaultPolling, emptyConfig, formatTime } from "./model";
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
      screen.getByRole("button", { name: "Save & activate" }),
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
    await screen.findByRole("heading", { name: "Task to create" });
    expect(
      screen.getByRole("heading", { name: "When to create a task" }),
    ).toBeTruthy();
    expect((screen.getByLabelText("Prompt") as HTMLTextAreaElement).value).toBe(
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
    fireEvent.change(screen.getByLabelText("Your Arcanum login"), {
      target: { value: "reviewer" },
    });
    fireEvent.change(screen.getByLabelText("Task title"), {
      target: { value: "Review {{pr_id}}" },
    });
    fireEvent.change(screen.getByLabelText("Prompt"), {
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
    await screen.findByLabelText("Prompt");
    fireEvent.change(screen.getByLabelText("Prompt"), {
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
it("retains the source draft's captured version across an external update", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const original = sourceTask();
  const save = vi.fn(
    async (_p: unknown, _enabled: boolean, version: number) => version + 1,
  );
  const view = render(
    <SourceEditor task={original} busy={false} onSave={save} />,
  );
  fireEvent.change(screen.getByLabelText("Prompt"), {
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
  expect(save.mock.calls[0][2]).toBe(3);
  expect(save.mock.calls[0][0]).toMatchObject({
    action: { child_definition: { config: { initial_prompt: "My draft" } } },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save source" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][2]).toBe(4);
});

it("pauses a source independently while preserving its unsaved child draft", async () => {
  rows = [sourceTask()];
  window.history.replaceState(null, "", "/?task=source");
  render(<App />);
  await screen.findByLabelText("Prompt");
  fireEvent.change(screen.getByLabelText("Prompt"), {
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
  expect((screen.getByLabelText("Prompt") as HTMLTextAreaElement).value).toBe(
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
  const save = vi.fn(async (_polling: Polling) => 4);
  render(<SourceEditor task={sourceTask()} busy={false} onSave={save} />);
  fireEvent.click(screen.getByLabelText("Repeat work"));
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
  const save = vi.fn(async (_p: Polling) => 4);
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
  expect(await screen.findByText(/reviewer or author/)).toBeTruthy();
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
  const save = vi.fn(async () => 1);
  render(<SourceEditor busy={false} onSave={save} />);
  fireEvent.change(screen.getByLabelText("Polling type"), {
    target: { value: "cron" },
  });
  fireEvent.click(screen.getByLabelText("Repeat work"));
  expect((screen.getByLabelText("PR ID") as HTMLInputElement).value).toBe("");
  expect((screen.getByLabelText("PR author") as HTMLInputElement).value).toBe(
    "",
  );
});

it("edits initial launch policy independently of execution approvals", async () => {
  const { SourceEditor } = await import("./SourceEditor");
  const save = vi.fn(async (_p: unknown) => 4);
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
  await screen.findByRole("heading", { name: "Other task" });
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
  await waitFor(() => expect(screen.getByRole("heading", { name: "Other task" })).toBeTruthy());
  expect(screen.queryByRole("heading", { name: "Review the next revision" })).toBeNull();
});
it("preserves an unsaved source prompt when background details refresh fails and retries", async () => {
  rows = [sourceTask()];
  window.history.replaceState(null, "", "/?task=source");
  render(<App />);
  const prompt = await screen.findByLabelText("Prompt");
  fireEvent.change(prompt, { target: { value: "Unsaved draft" } });
  const original = vi.mocked(fetch).getMockImplementation()!;
  let failed = true;
  vi.mocked(fetch).mockImplementation(async (path, options) => {
    if (path === "/v1/tasks/source" && failed) return { ok: false, status: 503, json: async () => ({ error: "Details refresh unavailable" }) } as Response;
    return original(path, options);
  });
  fireEvent.focus(window);
  await screen.findByText("Details refresh unavailable");
  expect((screen.getByLabelText("Prompt") as HTMLTextAreaElement).value).toBe("Unsaved draft");
  expect(screen.getByLabelText("Prompt")).toBe(prompt);
  failed = false;
  fireEvent.click(screen.getByRole("button", { name: "Retry details" }));
  await waitFor(() => expect(screen.queryByText("Details refresh unavailable")).toBeNull());
  expect((screen.getByLabelText("Prompt") as HTMLTextAreaElement).value).toBe("Unsaved draft");
  expect(screen.getByLabelText("Prompt")).toBe(prompt);
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
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
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
  await waitFor(() => expect((screen.getByRole("button", { name: "Save task execution" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest task execution" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save task execution" }));
  await waitFor(() => expect(rows[0].config.initial_prompt).toBe("Changed prompt"));
  expect(rows[0].title).toBe("New title");
  await waitFor(() => expect((screen.getByRole("button", { name: "Save polling" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest polling" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save polling" }));
  await waitFor(() => expect(rows[0].polling?.config.pr_id).toBe("123"));
  fireEvent.change(screen.getByLabelText("Initial prompt"), { target: { value: "Another draft" } });
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "456" } });
  await waitFor(() => expect((screen.getByRole("button", { name: "Save polling" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest polling" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save polling" }));
  await waitFor(() => expect(rows[0].polling?.config.pr_id).toBe("456"));
  expect((screen.getByLabelText("Initial prompt") as HTMLTextAreaElement).value).toBe("Another draft");
  await waitFor(() => expect((screen.getByRole("button", { name: "Save task execution" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest task execution" })).toBeNull());
  fireEvent.click(screen.getByRole("button", { name: "Save task execution" }));
  await waitFor(() => expect(rows[0].config.initial_prompt).toBe("Another draft"));
});
it("preserves overview drafts and blocks replacement of externally edited fields", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Local draft" } });
  rows = [makeTask({ version: 2, updated_at: "2026-01-02T12:00:00Z", config: { ...emptyConfig(), initial_prompt: "External prompt" } })];
  fireEvent.focus(window);
  await waitFor(() => expect(calls.filter(c => c.path === "/v1/tasks/task-1").length).toBeGreaterThan(1));
  await waitFor(() => expect(screen.queryByText("Loading details…")).toBeNull());
  await screen.findByText(formatTime("2026-01-02T12:00:00Z"));
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Local draft");
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
  await waitFor(() => expect(rows[0].title).toBe("Local draft"));
  expect(rows[0].config.initial_prompt).toBe("External prompt");
  await waitFor(() => expect((screen.getByRole("button", { name: "Save overview" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "My description" } });
  rows = [{ ...rows[0], version: 4, description: "External description" }];
  fireEvent.focus(window);
  await screen.findByRole("button", { name: "Reload latest overview" });
  expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("My description");
  const writes = calls.filter(c => c.method === "PATCH").length;
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
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
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Typing while saving" } });
  rows = [makeTask({ title: "Concurrent title", version: 3 })];
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
  await screen.findByRole("button", { name: "Reload latest overview" });
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Typing while saving");
  await waitFor(() => expect((screen.getByRole("button", { name: "Save overview" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
  expect(rows[0].title).toBe("Concurrent title");
});

it("does not bless a concurrent polling save reply", async () => {
  const initial = { ...defaultPolling(), config: { ...defaultPolling().config, pr_id: "42", login: "reviewer" } };
  const concurrent = { ...initial, config: { ...initial.config, pr_id: "44" } };
  const save = vi.fn(async () => ({ version: 3, polling: concurrent }));
  const { rerender } = render(<PollingForm initial={initial} expectedVersion={1} onSave={save} />);
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "43" } });
  fireEvent.click(screen.getByRole("button", { name: "Save polling" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  rerender(<PollingForm initial={concurrent} expectedVersion={3} onSave={save} />);
  await screen.findByRole("button", { name: "Reload latest polling" });
  fireEvent.click(screen.getByRole("button", { name: "Save polling" }));
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
  expect(screen.getByLabelText("High priority")).toBeTruthy();
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
  await waitFor(() => expect((screen.getByRole("button", { name: "Save polling" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("PR ID"), { target: { value: "43" } });
  fireEvent.change(screen.getByLabelText("Your Arcanum login"), { target: { value: "reviewer" } });
  fireEvent.keyDown(screen.getByLabelText("PR ID"), { key: "Enter", metaKey: true });
  await waitFor(() => expect(rows[0].polling?.config.pr_id).toBe("43"));
});
it("preserves new overview typing during an ordinary save and saves sources with Cmd+Enter", async () => {
  window.history.replaceState(null, "", "/?task=task-1");
  const original = vi.mocked(fetch).getMockImplementation()!;
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>(done => { resolve = done; });
  vi.mocked(fetch).mockImplementation((path, options) => options?.method === "PATCH" ? held : original(path, options));
  const view = render(<App />);
  fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Submitted title" } });
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
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
  const prompt = await screen.findByLabelText("Prompt");
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
  const codex = screen.getByRole("button", { name: "Codex: not linked" });
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
  await waitFor(() => expect((screen.getByRole("button", { name: "Save overview" }) as HTMLButtonElement).disabled).toBe(false));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Reload latest overview" })).toBeNull());
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Unsaved text");
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
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
  await waitFor(() => expect((screen.getByRole("button", { name: "Save overview" }) as HTMLButtonElement).disabled).toBe(false));
  rejectPriority = false;
  fireEvent.change(screen.getByLabelText("Priority for Review the next revision"), { target: { value: "high" } });
  await waitFor(() => expect((screen.getByRole("button", { name: "Save overview" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Save overview" }));
  await waitFor(() => expect(calls.some(call => call.method === "PATCH")).toBe(true));
  expect(calls.find(call => call.method === "PATCH")?.body).toMatchObject({ expected_version: 1 });
  // The compact reply must not grant stale full settings its newer version.
  rows = [makeTask({ priority: "high", version: 3, config: { ...emptyConfig(), initial_prompt: "Unseen newer prompt" } })];
  resolve({ ok: true, status: 200, json: async () => rows[0] } as Response);
});
