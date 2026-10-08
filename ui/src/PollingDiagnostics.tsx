import type { Task } from "./model";
import { formatTime, pollingLabel } from "./model";
export function PollingDiagnostics({ task }: { task: Task }) {
  const p = task.polling;
  return (
    <section className="poll-diagnostics">
      <div className="diagnostic-heading">
        <span
          className={`connection-dot ${task.column === "polling" ? "live" : ""}`}
        />
        <strong>{pollingLabel(task)}</strong>
      </div>
      {p ? (
        <>
          <p>
            {p.type === "pr_review_source"
              ? "PR review"
              : p.type === "pr_changes_source"
                ? "My PRs requesting changes"
                : p.type === "cron"
                  ? "Cron schedule"
                  : "PR waiting"}{" "}
            · {p.mode}
          </p>
          <dl className="metadata">
            <div>
              <dt>Next check</dt>
              <dd>
                {task.column === "polling"
                  ? formatTime(p.next_check_at)
                  : "Paused"}
              </dd>
            </div>
            <div>
              <dt>Last attempt</dt>
              <dd>{formatTime(p.last_attempt_at)}</dd>
            </div>
            <div>
              <dt>Last success</dt>
              <dd>{formatTime(p.last_success_at)}</dd>
            </div>
          </dl>
          {p.last_error && <p className="error-note">{p.last_error}</p>}
        </>
      ) : (
        <p>No polling settings. Add them in the Polling tab.</p>
      )}
    </section>
  );
}
