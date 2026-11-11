import { useState } from "react";
import type { Task, TaskCard } from "./model";
import { codexURL, compactTask, isSource, labels } from "./model";
import { ChipSelect } from "./ChipSelect";
import { Field } from "./forms";
import { ServiceIcon } from "./ServiceIcons";

export function TaskRelations({ task, cards, busy, onSelect, onSave }: {
  task: Task; cards: TaskCard[]; busy: boolean;
  onSelect: (card: TaskCard) => void;
  onSave: (pr: TaskCard, parentID: string) => Promise<unknown>;
}) {
  const [editing, setEditing] = useState(false);
  const [query, setQuery] = useState("");
  const [candidate, setCandidate] = useState<TaskCard>();
  const [baseline, setBaseline] = useState(task.version);
  const [error, setError] = useState("");
  const isPR = task.card_type === "pr";
  if (isSource(task) || task.card_type === "schedule") return null;
  const parent = cards.find(card => card.id === task.parent_task_id);
  const children = cards.filter(card => card.card_type === "pr" && card.parent_task_id === task.id);
  const candidates = cards.filter(card => !isSource(card) &&
    (isPR ? (card.card_type || "task") === "task" : card.card_type === "pr" && card.parent_task_id !== task.id) &&
    `${card.title} ${card.arcanum_pr_id || ""} ${card.ticket_url || ""}`.toLowerCase().includes(query.trim().toLowerCase()));
  const choices = candidate && !candidates.some(card => card.id === candidate.id) ? [candidate, ...candidates] : candidates;
  const options = [{ value: "", label: isPR ? "Choose a Task" : "Choose a PR" }, ...choices.map(card => ({ value: card.id, label: isPR ? card.title : `#${card.arcanum_pr_id} · ${card.title}` }))];
  const save = async () => {
    if (!candidate || busy) return;
    setError("");
    try {
      await onSave(isPR ? { ...compactTask(task), version: baseline } : candidate, isPR ? candidate.id : task.id);
      setEditing(false); setCandidate(undefined); setQuery("");
    } catch (err) { setError((err as Error).message); }
  };
  return <section className="task-relations" aria-label={isPR ? "Parent Task" : "Linked PRs"}>
    <div className="relation-heading"><h3>{isPR ? "Task" : "Pull requests"}</h3>
      <button type="button" className="relation-edit" disabled={busy} onClick={() => {
        setEditing(!editing); setBaseline(task.version); setError("");
      }}>{isPR && parent ? "Change Task" : isPR ? "Link Task" : "Link PR"}</button>
    </div>
    {isPR && parent && <div className="parent-task-link">
      <button type="button" onClick={() => onSelect(parent)}>{parent.title}<span aria-hidden="true">↗</span></button>
      {parent.thread_id && <a href={codexURL(parent.thread_id)} title="Open development chat" aria-label="Open development chat"><ServiceIcon service="codex" /></a>}
      <button type="button" className="relation-edit" disabled={busy} onClick={() => {
        setError(""); void onSave(compactTask(task), "").catch(err => setError((err as Error).message));
      }}>Unlink</button>
    </div>}
    {!isPR && children.map(pr => <button key={pr.id} type="button" className="linked-pr-row" onClick={() => onSelect(pr)}>
      <span>#{pr.arcanum_pr_id}</span><span>{labels[pr.column]}</span>
    </button>)}
    {editing && <div className="relation-form">
      <Field label={isPR ? "Find Task" : "Find PR"}><input value={query} onChange={e => setQuery(e.target.value)} placeholder={isPR ? "Title or Tracker ticket" : "PR number or title"} /></Field>
      <ChipSelect label={isPR ? "Parent Task" : "PR to link"} value={candidate?.id || ""} options={options} disabled={busy}
        onChange={id => setCandidate(cards.find(card => card.id === id))} />
      <div className="form-actions"><button type="button" disabled={busy || !candidate} onClick={() => void save()}>
        {!isPR && candidate?.parent_task_id ? "Move PR here" : isPR ? "Save Task link" : "Link PR card"}
      </button><button type="button" disabled={busy} onClick={() => setEditing(false)}>Cancel</button></div>
      {error && <button type="button" disabled={busy} onClick={() => {
        setBaseline(task.version); setCandidate(cards.find(card => card.id === candidate?.id)); setError("");
      }}>Reload latest link</button>}
    </div>}
    {error && <p role="alert" className="error-note">{error}</p>}
  </section>;
}
