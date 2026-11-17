import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { ReactNode } from "react";
import type { Task, TaskCard } from "./model";

type Result = {
  thread_id: string;
  fresh: boolean;
  state: string;
  error?: string;
};
export function CreateChat({ task, disabled = false, onBusyChange, leadingAction, trailingAction }: {
  task: Task | TaskCard;
  disabled?: boolean;
  onBusyChange?: (busy: boolean) => void;
  leadingAction?: ReactNode;
  trailingAction?: ReactNode;
}) {
  const pending = useRef(false);
  const attempt = useRef(crypto.randomUUID());
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Result>();
  const [error, setError] = useState("");
  const create = async () => {
    if (pending.current || disabled) return;
    pending.current = true;
    setBusy(true);
    onBusyChange?.(true);
    setError("");
    try {
      const r = await api<Result>(`/v1/tasks/${task.id}/create-chat`, "POST", {
        expected_version: task.version,
        attempt_id: attempt.current,
      });
      setResult(r);
      if (r.state === "ready" && r.fresh) {
        const link = `codex://threads/${encodeURIComponent(r.thread_id)}`;
        window.location.href = task.ticket_url
          ? `${link}?prompt=${encodeURIComponent(task.ticket_url)}`
          : link;
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      pending.current = false;
      setBusy(false);
      onBusyChange?.(false);
    }
  };
  return (
    <div>
      <div className="form-actions">
      {leadingAction}
      {!task.thread_id && (task.card_type || "task") === "task" && (
        <button className="primary" type="button" disabled={busy || disabled} onClick={() => void create()}>
          {busy
            ? "Creating chat…"
            : result?.state === "created"
              ? "Retry chat setup"
              : "Create chat"}
        </button>
      )}
      {trailingAction}
      </div>
      {result?.thread_id && (
        <a href={`codex://threads/${encodeURIComponent(result.thread_id)}`}>
          Open in Codex
        </a>
      )}
      {task.ticket_url && (
        <button
          type="button"
          onClick={() =>
            void navigator.clipboard
              .writeText(task.ticket_url || "")
              .catch(() => setError("Could not copy ticket URL"))
          }
        >
          Copy ticket URL
        </button>
      )}
      {(error || result?.error) && (
        <p role="alert">
          {error || result?.error}
          {result?.thread_id && ` · ${result.thread_id}`}
        </p>
      )}
    </div>
  );
}

export function FixChatName({
  task,
  parentVersion,
}: {
  task: Task;
  parentVersion?: number;
}) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let current = true;
    void api<{ name?: string }>(`/v1/tasks/${task.id}/chat-name`)
      .then((r) => {
        if (current) setName(r.name || "");
      })
      .catch(() => {});
    return () => {
      current = false;
    };
  }, [
    task.id,
    task.thread_id,
    task.parent_task_id,
    task.version,
    parentVersion,
  ]);
  if (!name) return null;
  return (
    <div>
      <span>{name}</span>
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          void api(`/v1/tasks/${task.id}/chat-name`, "POST", {})
            .then(() => {
              setName("");
              setError("");
            })
            .catch((e) => setError((e as Error).message))
            .finally(() => setBusy(false));
        }}
      >
        Apply template
      </button>
      {error && <p role="alert">{error}</p>}
    </div>
  );
}
