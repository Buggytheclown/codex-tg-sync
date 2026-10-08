import { describe, it, expect } from "vitest";
import {
  compactTask,
  defaultPolling,
  emptyConfig,
  executionLabel,
  pollingLabel,
  pollingProblem,
  safeExternalURL,
  parseThreadID,
} from "./model";
import type { Task } from "./model";
const task = (extra: Partial<Task> = {}): Task => ({
  id: "t",
  title: "Task",
  description: "",
  column: "todo",
  position: 1,
  thread_id: "",
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
describe("card states", () => {
  it("has no execution state before a run", () =>
    expect(executionLabel(task())).toBe(""));
  it("execution is independent of its column", () =>
    expect(
      executionLabel(
        task({
          column: "done",
          execution: { last_seen_turn_status: "inProgress" },
        }),
      ),
    ).toBe("Running"));
  it("distinguishes absent, paused and active polling", () => {
    expect(pollingLabel(task())).toBe("No polling");
    const polling = {
      ...defaultPolling("pr_wait"),
      config: {
        login: "reviewer",
        pr_id: "123",
        author: "author",
        conditions: ["new_commit"],
        interval_seconds: 60,
      },
    };
    expect(pollingLabel(task({ polling }))).toBe("Configured · paused");
    expect(pollingLabel(task({ column: "polling", polling }))).toBe("Waiting");
  });
  it("cannot activate an empty condition set", () => {
    const p = defaultPolling();
    p.config = {
      login: "me",
      pr_id: "123",
      author: "them",
      interval_seconds: 60,
      conditions: [],
    };
    expect(pollingProblem(p)).toContain("at least one");
  });
  it("allows source variables only in child waiting defaults", () => {
    const p = defaultPolling();
    p.config = {
      login: "me",
      pr_id: "{{pr_id}}",
      author: "{{author}}",
      interval_seconds: 60,
      conditions: ["new_commit"],
    };
    expect(pollingProblem(p, true)).toBe("");
    expect(pollingProblem(p)).toContain("numeric");
  });
  it("shows terminated failed/interrupted runs as Completed", () => {
    expect(
      executionLabel(task({ execution: { last_seen_turn_status: "failed" } })),
    ).toBe("Completed");
    expect(
      executionLabel(
        task({ execution: { last_seen_turn_status: "interrupted" } }),
      ),
    ).toBe("Completed");
  });
  it("accepts a Codex deep link when adding a chat", () =>
    expect(parseThreadID("codex://threads/thread-1")).toBe("thread-1"));
  it("rejects script URLs without restricting ordinary PR links", () => {
    expect(safeExternalURL("javascript:alert(1)")).toBeUndefined();
    expect(safeExternalURL("https://example.test/review/123")).toBe(
      "https://example.test/review/123",
    );
  });
});

it("requires a self-contained child definition for source polling", () => {
  const p = defaultPolling("pr_review_source");
  p.config = { login: "reviewer", interval_seconds: 60 };
  expect(pollingProblem(p)).toContain("title");
  p.action.child_definition = {
    title: "Review {{pr_id}}",
    description: "",
    config: { ...emptyConfig(), initial_prompt: "Review it" },
  };
  expect(pollingProblem(p)).toBe("");
});

it("rejects recursive source definitions and repeat waiting", () => {
  const p = defaultPolling("pr_review_source");
  p.config = { login: "reviewer", interval_seconds: 60 };
  p.action.child_definition = {
    title: "Child",
    description: "",
    config: { ...emptyConfig(), initial_prompt: "Work" },
    polling: defaultPolling("cron"),
  };
  expect(pollingProblem(p)).toContain("one PR once");
  const wait = defaultPolling();
  wait.config = {
    pr_id: "0",
    conditions: ["new_commit"],
    interval_seconds: 60,
  };
  expect(pollingProblem(wait)).toContain("numeric");
  wait.config.pr_id = "123";
  wait.mode = "repeat";
  expect(pollingProblem(wait)).toContain("once");
});

it("requires explicit PR coordinates when a source uses a schedule", () => {
  const p = defaultPolling("cron");
  p.action.child_definition = {
    title: "Scheduled work",
    description: "",
    config: {
      ...emptyConfig(),
      initial_prompt: "Work",
      continuation_prompt: "Follow up",
    },
    polling: {
      ...defaultPolling(),
      config: {
        pr_id: "{{pr_id}}",
        author: "{{author}}",
        login: "reviewer",
        conditions: ["new_commit"],
        interval_seconds: 60,
      },
    },
  };
  expect(pollingProblem(p)).toContain("explicit positive PR ID");
  const wait = p.action.child_definition.polling!;
  wait.config.pr_id = "123";
  expect(pollingProblem(p)).toContain("reviewer or author");
  wait.config.author = "";
  wait.config.login = "{{login}}";
  expect(pollingProblem(p)).toContain("reviewer or author");
  wait.config.login = "reviewer";
  expect(pollingProblem(p)).toBe("");
});

it("projects full mutation tasks to cards without retaining snapshots or prompts", () => {
  const full = { ...task(), private_payload: "large", execution: { last_seen_turn_status: "completed", snapshot: { prompt: "large secret prompt" } }, requests: [{ id: "r", status: "starting", prompt: "frozen", created_at: "" }], polling: defaultPolling("cron") };
  const card = compactTask(full);
  expect(card).not.toHaveProperty("config");
  expect(card).not.toHaveProperty("private_payload");
  expect(card.execution).not.toHaveProperty("snapshot");
  expect(card.polling).not.toHaveProperty("config");
  expect(card.polling?.action).toEqual({ type: "create_task" });
  expect(card.requests).toEqual([{ id: "r", status: "starting" }]);
});
