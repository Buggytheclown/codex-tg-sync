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
import { defaultPolling, emptyConfig } from "./model";
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
        path === "/v1/tasks" ? rows : path.endsWith("/events") ? [] : {};
      if (method === "PUT" && path.endsWith("/polling")) {
        const id = path.split("/")[3];
        const task = rows.find((t) => t.id === id)!;
        payload = { ...task, polling: body.polling, version: task.version + 1 };
        rows = rows.map((t) => (t.id === id ? (payload as Task) : t));
      }
      if (method === "POST" && path === "/v1/tasks") {
        payload = makeTask({ id: "new-source", ...body });
        rows = [...rows, payload as Task];
      }
      if (method === "POST" && path.endsWith("/move")) {
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
