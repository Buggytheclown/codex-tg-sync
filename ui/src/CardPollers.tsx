import { eventLabels } from "./EventGroups";
import { formatTime } from "./model";
import type { CardPollingRule, Column, PollingSummary } from "./model";
import { ScheduleIcon, SourceIcon } from "./ServiceIcons";

const ruleTypes: Record<string, string> = {
  cron: "Cron",
  pr_wait: "Arcanum PR",
  pr_review_source: "PR review",
  pr_changes_source: "PR feedback",
};

function ruleLabel(rule: CardPollingRule) {
  const groups = rule.condition_groups?.filter(group => group.length) || [];
  return groups.map(group => {
    const terms = group.map(term => term === "merge_checks_failed" ? "Failed merge checks" : eventLabels[term] || term).join(" and ");
    return groups.length > 1 && group.length > 1 ? `(${terms})` : terms;
  }).join(" or ") || ruleTypes[rule.type] || rule.title || "Polling rule";
}

function countdown(scheduledAt: string, now: number) {
  const remaining = Math.ceil((Date.parse(scheduledAt) - now) / 60_000);
  if (remaining <= 0) return "Due now";
  if (remaining < 60) return `in ${remaining} min`;
  const hours = Math.floor(remaining / 60);
  if (hours < 24) return `in ${hours}h${remaining % 60 ? ` ${remaining % 60}m` : ""}`;
  return `in ${Math.floor(hours / 24)}d${hours % 24 ? ` ${hours % 24}h` : ""}`;
}

export function CardPollers({ polling, column }: { polling?: PollingSummary; column: Column }) {
  if (!polling) return <span className="poll-chip">No polling</span>;
  const paused = column !== "polling";
  const status = paused ? "Paused" : polling.problem ? "Needs configuration" : polling.last_error ? "Active · check failed" : "Active";
  const rules = polling.rules || (polling.type === "rules" ? [] : [{ type: polling.type }]);
  const multiplePRs = new Set(rules.map(rule => rule.pr_id).filter(Boolean)).size > 1;
  const now = Date.now();
  return <span className={`card-pollers${paused ? " paused" : ""}`}>
    <span className="card-pollers-heading">
      <span>Pollers</span>
      <span className={`card-pollers-state${!paused && status !== "Active" ? " problem" : ""}`}
        title={paused ? undefined : polling.problem || polling.last_error || undefined}>
        {paused
          ? <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><path d="M6 4v8m4-8v8" /></svg>
          : <i aria-hidden="true" />}
        {status}
      </span>
    </span>
    {rules.length > 0 && <span className="card-poller-rules">
      {rules.map((rule, index) => {
        const scheduledAt = !paused && !polling.problem && rule.type === "cron" && rule.next_scheduled_at;
        const next = scheduledAt && Number.isFinite(Date.parse(scheduledAt)) ? scheduledAt : undefined;
        const label = ruleLabel(rule);
        return <span className={`card-poller-rule${label.length > 25 ? " wide" : ""}`} key={index} title={[rule.title, rule.pr_id && `PR #${rule.pr_id}`].filter(Boolean).join(" · ") || undefined}>
          {rule.type === "cron" ? <ScheduleIcon /> : label === "PR merged"
            ? <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true"><path d="M6 8v8m12-2c-7 0-12-2-12-6" /><circle cx="6" cy="5" r="3" /><circle cx="6" cy="19" r="3" /><circle cx="18" cy="17" r="3" /></svg>
            : <SourceIcon kind={rule.type === "pr_review_source" ? "review" : "feedback"} />}
          <span>{label}{multiplePRs && rule.pr_id ? ` · #${rule.pr_id}` : ""}
            {next && <> · <time dateTime={next} title={formatTime(next)}>{countdown(next, now)}</time></>}
          </span>
        </span>;
      })}
    </span>}
  </span>;
}
