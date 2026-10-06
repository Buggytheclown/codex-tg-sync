import { describe, it, expect } from "vitest";
import {
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
  template_id: "",
  template_version: 0,
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
  it("allows template variables only in template child wait", () => {
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
