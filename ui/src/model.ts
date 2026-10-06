export const columns = ["inbox", "todo", "polling", "done"] as const;
export type Column = (typeof columns)[number];
export const labels: Record<Column, string> = {
  inbox: "Inbox",
  todo: "TODO",
  polling: "Polling",
  done: "Done",
};
export type ExecutionSettings = {
  cwd: string;
  model: string;
  reasoning_effort: string;
  approval_policy: string;
  approvals_reviewer: string;
  sandbox_mode: string;
};
export type TaskConfig = {
  initial_prompt: string;
  continuation_prompt: string;
  execution: ExecutionSettings;
};
export type PollConfig = {
  login?: string;
  pr_id?: string;
  author?: string;
  conditions?: string[];
  interval_seconds?: number;
  cron?: string;
  timezone?: string;
  max_lateness?: string;
};
export type Polling = {
  task_id?: string;
  type: string;
  mode: string;
  config: PollConfig;
  action: { type: string; template_id?: string };
  cursor?: unknown;
  generation?: number;
  next_check_at?: string;
  last_attempt_at?: string;
  last_success_at?: string;
  last_error?: string;
};
export type RequestInfo = {
  id: string;
  status: string;
  prompt: string;
  error_summary?: string;
  thread_id?: string;
  created_at: string;
  target_thread_id?: string;
};
export type Task = {
  id: string;
  title: string;
  description: string;
  column: Column;
  position: number;
  thread_id: string;
  source_url: string;
  source_key: string;
  template_id: string;
  template_version: number;
  config: TaskConfig;
  version: number;
  created_at: string;
  updated_at: string;
  polling?: Polling;
  execution?: {
    last_seen_turn_status?: string;
    last_seen_thread_status?: string;
    last_poll_at?: string;
  };
  requests: RequestInfo[];
};
export type Definition = {
  title: string;
  description: string;
  config: TaskConfig;
  polling?: Polling;
};
export type Template = {
  id: string;
  name: string;
  version: number;
  definition: Definition;
  updated_at: string;
};
export type TaskEvent = {
  id: string;
  task_id: string;
  kind: string;
  event_key: string;
  created_at: string;
  result_task_id: string;
  payload: unknown;
};
export const emptyConfig = (): TaskConfig => ({
  initial_prompt: "",
  continuation_prompt: "",
  execution: {
    cwd: "",
    model: "",
    reasoning_effort: "",
    approval_policy: "on-request",
    approvals_reviewer: "auto_review",
    sandbox_mode: "workspace-write",
  },
});
export const defaultPolling = (type = "pr_wait"): Polling => ({
  type,
  mode: type === "pr_wait" ? "once" : "repeat",
  config:
    type === "cron"
      ? {
          cron: "0 9 * * 1",
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
          max_lateness: "5m",
        }
      : {
          login: "",
          ...(type === "pr_wait"
            ? {
                pr_id: "",
                author: "",
                conditions: ["new_commit", "all_answered"],
              }
            : {}),
          interval_seconds: 60,
        },
  action: { type: type === "pr_wait" ? "return_task" : "create_task" },
});
export const isSource = (task: Task) =>
  task.polling?.action.type === "create_task";
export const executionLabel = (task: Task): string => {
  const status = task.execution?.last_seen_turn_status?.toLowerCase();
  if (
    status === "inprogress" ||
    status === "in_progress" ||
    status === "running"
  )
    return "Running";
  if (status === "completed") return "Completed";
  if (status === "failed" || status === "interrupted") return "Completed";
  return "";
};
export function pollingProblem(p?: Polling, template = false): string {
  if (!p) return "Add a polling configuration before moving to Polling.";
  if (
    !["pr_review_source", "pr_changes_source", "pr_wait", "cron"].includes(
      p.type,
    )
  )
    return "Choose a supported polling type.";
  if (!["once", "repeat"].includes(p.mode)) return "Choose a polling mode.";
  if (!["return_task", "resume_task", "create_task"].includes(p.action.type))
    return "Choose an action.";
  if (p.action.type === "create_task" && !p.action.template_id)
    return "Choose the template for created tasks.";
  if (p.type === "cron") {
    if (!p.config.cron?.trim() || !p.config.timezone?.trim())
      return "Enter a cron schedule and timezone.";
    if (p.mode !== "repeat" || p.action.type !== "create_task")
      return "Cron sources use Repeat and Create task.";
  } else {
    if (
      (p.type !== "pr_wait" || p.config.conditions?.includes("all_answered")) &&
      !p.config.login?.trim()
    )
      return "Enter your Arcanum login.";
    if (
      !p.config.interval_seconds ||
      p.config.interval_seconds < 10 ||
      p.config.interval_seconds > 86400
    )
      return "Enter a check interval between 10 and 86400 seconds.";
    if (p.type === "pr_wait") {
      if (
        !p.config.pr_id?.trim() ||
        (!template && !/^\d+$/.test(p.config.pr_id))
      )
        return "Enter the numeric PR ID.";
      if (!p.config.conditions?.length) return "Select at least one condition.";
    } else if (p.mode !== "repeat" || p.action.type !== "create_task")
      return "PR sources use Repeat and Create task.";
  }
  return "";
}
export function pollingLabel(task: Task): string {
  if (!task.polling) return "No polling";
  if (task.column !== "polling") return "Configured · paused";
  if (pollingProblem(task.polling)) return "Needs configuration";
  return task.polling.last_error ? "Waiting · check failed" : "Waiting";
}
export function safeExternalURL(raw: string): string | undefined {
  try {
    const url = new URL(raw);
    if (["http:", "https:"].includes(url.protocol)) return url.href;
  } catch {
    /* invalid link stays plain text */
  }
}
export const codexURL = (id: string) =>
  `codex://threads/${encodeURIComponent(id)}`;
export const formatTime = (raw?: string) =>
  raw
    ? new Date(raw).toLocaleString([], {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";

export function parseThreadID(raw: string): string {
  const value = raw.trim();
  if (!value.startsWith("codex:")) return value;
  try {
    const url = new URL(value);
    if (
      url.protocol === "codex:" &&
      url.hostname === "threads" &&
      url.pathname.split("/").filter(Boolean).length === 1
    )
      return decodeURIComponent(url.pathname.slice(1));
  } catch {}
  return "";
}
