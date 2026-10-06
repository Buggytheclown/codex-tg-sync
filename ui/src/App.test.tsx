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
import { emptyConfig } from "./model";
import type { Task, Template } from "./model";
const makeTask = (extra: Partial<Task> = {}): Task => ({
  id: "task-1",
  title: "Review the next revision",
  description: "Check comments",
  column: "todo",
  position: 1,
  thread_id: "thread-1",
  source_url: "",
  source_key: "",
  template_id: "",
  template_version: 0,
  config: emptyConfig(),
  version: 1,
  created_at: "",
  updated_at: "",
  requests: [],
  ...extra,
});
let rows: Task[],
  templates: Template[],
  calls: { path: string; method: string; body: unknown }[];
beforeEach(() => {
  rows = [makeTask()];
  templates = [];
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
      const payload =
        path === "/v1/tasks"
          ? rows
          : path === "/v1/task-templates"
            ? templates
            : path.endsWith("/events")
              ? []
              : {};
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
          action: { type: "create_task", template_id: "tpl" },
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
  it("distinguishes future template edits from copied task overrides", async () => {
    render(<App />);
    await screen.findByRole("button", {
      name: /CHAT Review the next revision/,
    });
    fireEvent.click(screen.getByRole("button", { name: /Templates/ }));
    expect(
      screen.getByText(/Template changes affect future tasks/),
    ).toBeTruthy();
    expect(screen.getByLabelText("Initial prompt")).toBeTruthy();
    expect(screen.getByLabelText("Continuation prompt")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: /JSON/ })).toBeNull();
  });
  it("creates from the template id so the server copies it atomically", async () => {
    templates = [
      {
        id: "tpl",
        name: "Review",
        version: 3,
        updated_at: "",
        definition: {
          title: "Review PR",
          description: "Copied review settings",
          config: emptyConfig(),
        },
      },
    ];
    render(<App />);
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /Templates/ }).textContent,
      ).toContain("1"),
    );
    fireEvent.click(screen.getByRole("button", { name: /New task/ }));
    fireEvent.change(screen.getByLabelText("Template"), {
      target: { value: "tpl" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create task" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      template_id: "tpl",
      column: "todo",
      title: "",
    });
  });
});

describe("template concurrency", () => {
  it("keeps the version captured with the draft when SSE supplies another edit", async () => {
    const { TemplateEditor } = await import("./App");
    const original: Template = {
      id: "tpl",
      name: "Review",
      version: 3,
      updated_at: "",
      definition: {
        title: "Review PR",
        description: "",
        config: emptyConfig(),
      },
    };
    const save = vi.fn(async (_t: Template | undefined, input: unknown) => ({
      ...original,
      version: (input as { expected_version: number }).expected_version + 1,
    }));
    const props = {
      template: original,
      templates: [original],
      busy: false,
      onSave: save,
      onDelete: async () => {},
    };
    const view = render(<TemplateEditor {...props} />);
    fireEvent.change(screen.getByLabelText("Template name"), {
      target: { value: "My draft" },
    });
    view.rerender(
      <TemplateEditor
        {...props}
        template={{ ...original, name: "Other browser", version: 4 }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][1]).toMatchObject({
      expected_version: 3,
      name: "My draft",
    });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
    expect(save.mock.calls[1][1]).toMatchObject({
      expected_version: 4,
      name: "My draft",
    });
  });
});

it("deletes against the captured editor version after a concurrent template edit", async () => {
  const { TemplateEditor } = await import("./App");
  const original: Template = {
    id: "tpl",
    name: "Review",
    version: 3,
    updated_at: "",
    definition: { title: "Review PR", description: "", config: emptyConfig() },
  };
  const remove = vi.fn(async (_template: Template) => {});
  const props = {
    template: original,
    templates: [original],
    busy: false,
    onSave: async () => original,
    onDelete: remove,
  };
  const view = render(<TemplateEditor {...props} />);
  view.rerender(
    <TemplateEditor
      {...props}
      template={{ ...original, name: "Other browser", version: 4 }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm delete" }));
  await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
  expect(remove.mock.calls[0][0]).toMatchObject({ id: "tpl", version: 3 });
});
