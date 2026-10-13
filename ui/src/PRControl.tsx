import { useEffect, useRef, useState } from "react";
import type { FormEvent, MouseEvent } from "react";
import type { Task, TaskCard } from "./model";
import { arcanumReviewLink } from "./model";
import { Field } from "./forms";
import { EditIcon, ServiceIcon } from "./ServiceIcons";

type PRTask = Pick<TaskCard, "id" | "version" | "source_url">;
type SavePR = (url: string, version: number) => Promise<Task>;

function normalizePR(value: string): string | undefined {
  const text = value.trim();
  if (!text) return "";
  const id = text.match(/^#?([1-9]\d*)$/)?.[1];
  if (id) return `https://a.yandex-team.ru/review/${id}`;
  const pr = arcanumReviewLink(text);
  return pr && /^[1-9]\d*$/.test(pr.id) ? pr.href : undefined;
}

export function PRControl({ task, busy, onSave }: { task: PRTask; busy: boolean; onSave?: SavePR }) {
  const [editing, setEditing] = useState(false);
  const pr = arcanumReviewLink(task.source_url);
  // Open before the form's autofocus blurs Overview. Its save feedback may
  // move this button between pointer down and click if focus moves earlier.
  const preserveFocus = (e: MouseEvent<HTMLButtonElement>) => { if (e.button === 0) e.preventDefault(); };
  return <div className="ticket-control pr-control">
    <div className="ticket-actions">
      {pr ? <>
        <a className="card-footer-link source-pr-link" aria-label={`Arcanum PR #${pr.id}`} href={pr.href} title={`Arcanum PR #${pr.id}`} target="_blank" rel="noreferrer" draggable={false}>
          <ServiceIcon service="arcanum" className="ticket-brand" /><span>#{pr.id}</span>
        </a>
        <button className="card-footer-link ticket-edit" type="button" aria-label="Edit PR" title="Edit PR" aria-expanded={editing} disabled={busy || !onSave} onMouseDown={preserveFocus} onClick={() => setEditing(true)}><EditIcon /></button>
      </> : <button className="card-footer-link connection-empty" type="button" title="Add PR" aria-expanded={editing} disabled={busy || !onSave} onMouseDown={preserveFocus} onClick={() => setEditing(true)}><ServiceIcon service="arcanum" className="connection-empty-icon" />Add PR</button>}
    </div>
    {editing && onSave && <PRForm task={task} busy={busy} onSave={onSave} onClose={() => setEditing(false)} />}
  </div>;
}

function PRForm({ task, busy, onSave, onClose }: { task: PRTask; busy: boolean; onSave: SavePR; onClose: () => void }) {
  const [baseline, setBaseline] = useState(task);
  const [value, setValue] = useState(task.source_url);
  const draft = useRef(value);
  const [error, setError] = useState("");
  useEffect(() => {
    if (task.version > baseline.version && task.source_url === baseline.source_url) setBaseline(task);
  }, [task, baseline]);
  const stale = baseline.version !== task.version || baseline.source_url !== task.source_url;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (stale) { setError("The PR changed. Reload latest PR before saving; your draft is preserved."); return; }
    const submitted = normalizePR(value);
    if (submitted === undefined) { setError("Enter a positive PR ID or an Arcanum review URL without credentials."); return; }
    setError("");
    try {
      const saved = await onSave(submitted, baseline.version);
      if (saved.source_url !== submitted) {
        setBaseline({ ...baseline, source_url: submitted, version: baseline.version + 1 });
        setError("The PR changed after saving. Reload latest PR before saving again.");
        return;
      }
      setBaseline(saved);
      if (draft.current === value) onClose();
    } catch (err) { setError((err as Error).message); }
  };
  return <form className="ticket-form pr-form" onSubmit={e => void submit(e)}>
    <Field label="PR ID or URL" hint="PR ID or Arcanum review link. Clear to remove the link.">
      <input type="text" autoFocus value={value} onChange={e => { draft.current = e.target.value; setValue(e.target.value); }} />
    </Field>
    {stale && <button type="button" disabled={busy} onClick={() => { setBaseline(task); setValue(task.source_url); draft.current = task.source_url; setError(""); }}>Reload latest PR</button>}
    {error && <p role="alert" className="error-note">{error}</p>}
    <div className="form-actions">
      <button disabled={busy}>Save PR</button>
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
    </div>
  </form>;
}
