import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import type { TaskCard } from "./model";
import { safeExternalURL } from "./model";
import { Field } from "./forms";

type TicketTask = Pick<TaskCard, "id" | "title" | "version" | "ticket_url">;
type SaveTicket = (url: string, version: number) => Promise<TaskCard>;
function validTicketURL(value: string): boolean {
  if (!/^https?:\/\//i.test(value)) return false;
  try {
    const url = new URL(value);
    return !!url.hostname && !url.username && !url.password;
  } catch { return false; }
}

export function TicketControl({ task, busy, onSave }: {
  task: TicketTask;
  busy: boolean;
  onSave: SaveTicket;
}) {
  const [editing, setEditing] = useState(false);
  const url = task.ticket_url || "";
  const href = safeExternalURL(url);
  return <div className="ticket-control">
    <div className="ticket-actions">
      {href ? <>
        <a href={href} target="_blank" rel="noreferrer" draggable={false}>Open ticket</a>
        <button type="button" disabled={busy} onClick={() => setEditing(true)}>Edit ticket</button>
      </> : <button type="button" disabled={busy} onClick={() => setEditing(true)}>{url ? "Edit ticket" : "Add ticket"}</button>}
    </div>
    {editing && <TicketForm task={task} busy={busy} onSave={onSave} onClose={() => setEditing(false)} />}
  </div>;
}

function TicketForm({ task, busy, onSave, onClose }: {
  task: TicketTask;
  busy: boolean;
  onSave: SaveTicket;
  onClose: () => void;
}) {
  const [baseline, setBaseline] = useState(task);
  const [value, setValue] = useState(task.ticket_url || "");
  const draft = useRef(value);
  const [error, setError] = useState("");
  useEffect(() => {
    if (task.version > baseline.version && (task.ticket_url || "") === (baseline.ticket_url || "")) setBaseline(task);
  }, [task, baseline]);
  const stale = baseline.version !== task.version || (baseline.ticket_url || "") !== (task.ticket_url || "");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (stale) { setError("The ticket changed. Reload latest ticket before saving; your draft is preserved."); return; }
    const submitted = value.trim();
    if (submitted && !validTicketURL(submitted)) { setError("Enter an absolute HTTP or HTTPS ticket URL without credentials."); return; }
    setError("");
    try {
      const saved = await onSave(submitted, baseline.version);
      if (saved.version > baseline.version + 1 && (saved.ticket_url || "") !== submitted) {
        setBaseline({ ...baseline, ticket_url: submitted, version: baseline.version + 1 });
        setError("The ticket changed after saving. Reload latest ticket before saving again.");
        return;
      }
      setBaseline(saved);
      // Keep input typed while the request was in flight.
      if (draft.current === value) onClose();
    } catch (err) { setError((err as Error).message); }
  };
  return <form className="ticket-form" onSubmit={(e) => void submit(e)}>
    <Field label="Ticket URL" hint="HTTP or HTTPS link. Clear the field to remove the ticket.">
      <input type="url" autoFocus value={value} onChange={(e) => { draft.current = e.target.value; setValue(e.target.value); }} />
    </Field>
    {stale && <button type="button" disabled={busy} onClick={() => {
      setBaseline(task); setValue(task.ticket_url || ""); draft.current = task.ticket_url || ""; setError("");
    }}>Reload latest ticket</button>}
    {error && <p role="alert" className="error-note">{error}</p>}
    <div className="form-actions">
      <button disabled={busy}>Save ticket</button>
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
    </div>
  </form>;
}
