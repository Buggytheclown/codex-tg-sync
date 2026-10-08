import { describe, expect, it } from "vitest";
import { normalizeTicketInput, ticketLabel } from "./ticket";

describe("ticket input and labels", () => {
  it("expands a trimmed issue key to the stored Tracker URL", () => {
    expect(normalizeTicketInput(" task-123 ")).toBe("https://st.yandex-team.ru/TASK-123");
    expect(normalizeTicketInput(" task_team-123 ")).toBe("https://st.yandex-team.ru/TASK_TEAM-123");
    expect(normalizeTicketInput(" ")).toBe("");
    expect(normalizeTicketInput("https://tracker.example/TASK-123")).toBe("https://tracker.example/TASK-123");
  });
  it.each(["https:tracker.example/TASK-123", "//tracker.example/TASK-123", "https://user:password@tracker.example/TASK-123", "javascript:alert(1)", "TASK", "TASK-abc", "TASK-0", "A-B-1", "TASK-01"])("rejects invalid input %s", value => {
    expect(normalizeTicketInput(value)).toBeUndefined();
  });
  it("labels the final nonempty URL path segment without query or fragment", () => {
    expect(ticketLabel("https://tracker.example/issues/TASK-123/?view=full#comment")).toBe("TASK-123");
    expect(ticketLabel("https://tracker.example/")).toBe("tracker.example");
    expect(ticketLabel("https://tracker.example/a%20b")).toBe("a b");
  });
});
