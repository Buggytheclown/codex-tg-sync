import { useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import type { Polling, Task } from './model';
import { defaultPolling, pollingProblem } from './model';
import { ExecutionFields, Field, PollingFields, TaskChatForm } from './forms';
import { PollingDiagnostics } from './PollingDiagnostics';

export function WorkflowStep({ number, title, tone = '', children }: { number: number | string; title: string; tone?: string; children: ReactNode }) {
  return <section className={`workflow-step ${tone}`}><span className="workflow-number" aria-hidden="true">{number}</span><h3>{title}</h3><div className="workflow-content">{children}</div></section>;
}
export function MergeCompletion({ value, onChange }: { value: Polling; onChange: (p: Polling) => void }) {
  return <label className="check-line"><input type="checkbox" aria-label="Move to Done when PR is merged" checked={value.config.complete_on_merge === true}
    onChange={e => onChange({ ...value, config: { ...value.config, complete_on_merge: e.target.checked } })} />Move to Done when PR is merged <span className="note">No model launch</span></label>;
}
export function WorkflowActions({ value, onChange }: { value: Polling; onChange: (p: Polling) => void }) {
  return <div className="workflow-actions" role="radiogroup" aria-label="After event">
    {[["return_task", "Return to Inbox", "No model launch"], ["resume_task", "Request continuation", "Requires this task’s Codex chat"]].map(([type, title, detail]) =>
      <label key={type} className={value.action.type === type ? 'selected' : ''}>
        <input type="radio" name="workflow-action" checked={value.action.type === type} onChange={() => onChange({ ...value, action: { ...value.action, type } })} />
        <span><strong>{title}</strong><small>{detail}</small></span>
      </label>)}
  </div>;
}
const editable = (task: Task) => JSON.stringify({ source_url: task.source_url, config: task.config, polling: task.polling ? { type: task.polling.type, mode: task.polling.mode, config: task.polling.config, action: task.polling.action } : undefined });
const owned = (task: Task, config: Task['config'], source_url: string, polling?: Polling): Task => ({ ...task, config, source_url, polling });
export function WorkflowForm({ task, busy, onSave, onActivate, onLink, configure = false }: {
  task: Task; busy: boolean; configure?: boolean;
  onSave: (input: { expected_version: number; title: string; description: string; source_url: string; config: Task['config']; polling?: Polling }) => Promise<Task>;
  onActivate: (task: Task) => Promise<Task>;
  onLink: (threadID: string, version: number) => Promise<Task>;
}) {
  const [baseline, setBaseline] = useState(task);
  const [config, setConfig] = useState(task.config);
  const [sourceURL, setSourceURL] = useState(task.source_url);
  const [waiting, setWaiting] = useState<Polling | undefined>(task.polling || (configure ? defaultPolling() : undefined));
  const [message, setMessage] = useState('');
  const [showChat, setShowChat] = useState(false);
  const submitting = useRef(false);
  const [pending, setPending] = useState(false);
  const disabled = busy || pending;
  useEffect(() => {
    if (task.version > baseline.version && editable(task) === editable(baseline)) setBaseline(task);
  }, [task, baseline]);
  useEffect(() => { if (configure) setWaiting(current => current || defaultPolling()); }, [configure]);
  const stale = task.version !== baseline.version;
  const missingChat = waiting?.action.type === 'resume_task' && !task.thread_id;
  const readiness = !waiting ? 'Configure waiting to activate it.' : pollingProblem(waiting) || (missingChat ? 'Codex chat required' : waiting.action.type === 'resume_task' && !config.continuation_prompt.trim() ? 'Enter a continuation prompt.' : '');
  const dirty = editable(owned(baseline, config, sourceURL, waiting)) !== editable(baseline);
  const reload = () => { setBaseline(task); setConfig(task.config); setSourceURL(task.source_url); setWaiting(task.polling); setMessage(''); };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (disabled || submitting.current) return;
    if (stale) { setMessage('Reload latest settings before saving. Your draft has been preserved.'); return; }
    const problem = waiting && pollingProblem(waiting);
    if (problem) { setMessage(problem); return; }
    // Missing chat is a readiness condition, so inactive settings can still be saved.
    if (task.column === 'polling' && waiting?.action.type === 'resume_task' && !config.continuation_prompt.trim()) { setMessage('Enter a continuation prompt.'); return; }
    const submitted = owned(baseline, config, sourceURL, waiting);
    setPending(true); submitting.current = true;
    try {
      const saved = await onSave({ expected_version: baseline.version, title: task.title, description: task.description, source_url: sourceURL, config, ...(waiting ? { polling: waiting } : {}) });
      if (saved.version > baseline.version + 1 && editable(saved) !== editable(submitted)) {
        setBaseline({ ...submitted, version: baseline.version + 1 });
        setMessage('This task changed after saving. Reload latest settings before saving again.');
      } else {
        setBaseline(saved);
        setConfig(current => JSON.stringify(current) === JSON.stringify(config) ? saved.config : current);
        setSourceURL(current => current === sourceURL ? saved.source_url : current);
        setWaiting(current => JSON.stringify(current) === JSON.stringify(waiting) ? saved.polling : current);
        setMessage('Settings saved.');
      }
    } catch (err) { setMessage((err as Error).message); }
    finally { submitting.current = false; setPending(false); }
  };
  const activate = async () => {
    if (disabled || submitting.current || stale || dirty || readiness) return;
    setPending(true); submitting.current = true;
    try {
      const saved = await onActivate(baseline);
      // A later authoritative reply must not certify unseen settings.
      if (editable(saved) !== editable(baseline)) setMessage('This task changed. Reload latest settings before saving.');
      else { setBaseline(saved); setMessage('Waiting is active.'); }
    } catch (err) { setMessage((err as Error).message); }
    finally { submitting.current = false; setPending(false); }
  };
  return <>
    <form className="workflow-editor" onSubmit={e => void save(e)}>
      <h2>Workflow steps</h2>
      <p className="note">Save settings, then activate waiting. ⌘/Ctrl + Enter saves.</p>
      <WorkflowStep number={1} title="Wait">
        {waiting ? <><PollingFields workflow value={waiting} onChange={setWaiting} />

        </> : <><p className="note">No waiting configured. You can save prompts and execution settings independently.</p><button type="button" disabled={disabled} onClick={() => setWaiting(defaultPolling())}>Configure waiting</button></>}
      </WorkflowStep>
      {waiting && <>
        <WorkflowStep number={2} title="After event">
          <WorkflowActions value={waiting} onChange={setWaiting} />
          {missingChat && <div className="readiness-banner" role="status"><strong>Codex chat required</strong><p>This action requires an existing Codex chat for this task.</p><div>
            <button type="button" disabled={disabled} onClick={() => setShowChat(true)}>Link chat</button>
            <button type="button" disabled={disabled} onClick={() => setWaiting({ ...waiting, action: { ...waiting.action, type: 'return_task' } })}>Use Return to Inbox</button>
          </div></div>}
        </WorkflowStep>
        {waiting.action.type === 'resume_task' && <WorkflowStep number={3} title="Continuation prompt">
          <Field label="Continuation prompt"><textarea required={task.column === "polling"} rows={3} value={config.continuation_prompt} onChange={e => setConfig({ ...config, continuation_prompt: e.target.value })} /></Field>
          <details className="form-section"><summary>Execution summary</summary><ExecutionFields compact value={config.execution} onChange={execution => setConfig({ ...config, execution })} /></details>
        </WorkflowStep>}
        <WorkflowStep number={waiting.action.type === 'resume_task' ? 4 : 3} title="Approval">
          <p className="approval-path">{waiting.action.type === 'resume_task' ? 'After event: Inbox → Telegram Start → same chat' : 'After event: Inbox. No approval or model launch.'}</p>
        </WorkflowStep>
        <WorkflowStep number="✓" title="PR merged → Done" tone="terminal"><MergeCompletion value={waiting} onChange={setWaiting} /></WorkflowStep>
      </>}
      <details className="form-section initial-launch"><summary>Initial launch · prompt and source</summary>
        <div className="section-content"><p className="note">Existing launch requests keep their frozen prompt and settings.</p><Field label="Initial prompt"><textarea rows={4} value={config.initial_prompt} onChange={e => setConfig({ ...config, initial_prompt: e.target.value })} /></Field>
          <Field label="Source URL" hint="Task provenance. Waiting uses the PR ID above."><input type="url" value={sourceURL} onChange={e => setSourceURL(e.target.value)} /></Field>
          {(!waiting || waiting.action.type !== 'resume_task') && <><Field label="Continuation prompt"><textarea rows={3} value={config.continuation_prompt} onChange={e => setConfig({ ...config, continuation_prompt: e.target.value })} /></Field>
            <details className="form-section"><summary>Working directory · Model · Permissions</summary><ExecutionFields compact value={config.execution} onChange={execution => setConfig({ ...config, execution })} /></details></>}
        </div>
      </details>
      {stale && <div className="readiness-banner"><p>This task changed. Your draft has been preserved.</p><button type="button" disabled={disabled} onClick={reload}>Reload latest settings</button></div>}
      {message && <p className="info-note" role="status">{message}</p>}
      <div className="form-actions workflow-footer"><button className="primary" disabled={disabled}>Save settings</button>
        <button type="button" disabled={disabled || stale || dirty || !!readiness || task.column === 'polling'} title={dirty ? 'Save settings before activating waiting.' : readiness || undefined} onClick={() => void activate()}>Activate waiting</button></div>
      <PollingDiagnostics task={task} />
    </form>
    {showChat && !task.thread_id && <div className="workflow-chat"><TaskChatForm task={task} busy={disabled} onLink={onLink} inline onCancel={() => setShowChat(false)} submitLabel="Attach chat" /></div>}
  </>;
}
