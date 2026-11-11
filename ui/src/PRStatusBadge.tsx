import type { TaskCard } from "./model";

export function PRStatusBadge({ task }: { task: TaskCard }) {
  if (task.card_type !== "pr") return null;
  const status = task.pr_status;
  const label = status?.label || (status?.unavailable ? "Unavailable" : "—");
  const title = status?.last_success_at
    ? `Last checked ${new Date(status.last_success_at).toLocaleString()}${status.stale ? " · awaiting a fresh observation" : ""}`
    : status?.unavailable ? "Arc status is currently unavailable" : "Arc status has not been checked yet";
  return <span className={`arc-status${status?.stale ? " stale" : ""}`} title={title}>
    {label}{status?.stale && <span className="arc-status-stale" aria-label="Arc status is stale">↻</span>}
  </span>;
}
