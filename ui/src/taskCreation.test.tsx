// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { NewTask } from "./App";
import { CreateChat, FixChatName } from "./CreateChat";
import { emptyConfig, type Task } from "./model";
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const task: Task = {
  id: "task",
  thread_id: "",
  source_url: "",
  source_key: "",
  card_type: "task",
  title: "Project Title",
  ticket_url: "https://st.yandex-team.ru/TASK-42",
  column: "todo",
  position: 1,
  version: 1,
  description: "",
  config: emptyConfig(),
  requests: [],
  created_at: "",
  updated_at: "",
};
it("keeps a manually edited title when Tracker finishes later", async () => {
  let resolve!: (r: unknown) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise((r) => (resolve = r))),
  );
  const save = vi.fn().mockResolvedValue(undefined);
  render(<NewTask busy={false} onSave={save} />);
  fireEvent.change(screen.getByLabelText("Tracker ticket"), {
    target: { value: "TASK-42" },
  });
  fireEvent.blur(screen.getByLabelText("Tracker ticket"));
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "My title" },
  });
  resolve({
    ok: true,
    json: async () => ({ summary: "Tracker title", url: task.ticket_url }),
  });
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe(
    "My title",
  );
  fireEvent.click(screen.getByRole("button", { name: "Create task" }));
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith(
      expect.objectContaining({ title: "My title", ticket_url: "TASK-42" }),
    ),
  );
});
it("autofills an empty title", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue({
        ok: true,
        json: async () => ({ summary: "Project title", url: task.ticket_url }),
      }),
  );
  render(<NewTask busy={false} onSave={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("Tracker ticket"), {
    target: { value: "TASK-42" },
  });
  fireEvent.blur(screen.getByLabelText("Tracker ticket"));
  await waitFor(() =>
    expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe(
      "Project title",
    ),
  );
});
it("opens a recovered chat with a plain URL", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue({
        ok: true,
        json: async () => ({ state: "ready", thread_id: "chat", fresh: false }),
      }),
  );
  render(<CreateChat task={task} />);
  fireEvent.click(screen.getByRole("button", { name: "Create chat" }));
  expect(
    (await screen.findByRole("link", { name: "Open in Codex" })).getAttribute(
      "href",
    ),
  ).toBe("codex://threads/chat");
});
it("refreshes a naming proposal when the parent ticket is added", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce({ ok: true, json: async () => ({}) })
    .mockResolvedValue({
      ok: true,
      json: async () => ({ name: "Фикс TASK-42 Project Title" }),
    });
  vi.stubGlobal("fetch", fetcher);
  const pr = {
    ...task,
    card_type: "pr" as const,
    thread_id: "chat",
    parent_task_id: "parent",
  };
  const view = render(<FixChatName task={pr} parentVersion={1} />);
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  view.rerender(<FixChatName task={pr} parentVersion={2} />);
  expect(
    await screen.findByRole("button", { name: "Apply template" }),
  ).toBeTruthy();
});
