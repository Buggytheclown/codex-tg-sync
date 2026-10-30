import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { Polling, Task } from './model';
import { pollingProblem, asRules, promptRules, newRules } from './model';
import { ExecutionFields, Field, TaskChatForm } from './forms';
import { PollingDiagnostics } from './PollingDiagnostics';
import { PollingRules } from './PollingRules';

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
  const [waiting, setWaiting] = useState<Polling | undefined>(task.polling ? asRules(task.polling, task.config.continuation_prompt) : (configure ? newRules() : undefined));
  const [message, setMessage] = useState('');
  const [showChat, setShowChat] = useState(false);
  const submitting = useRef(false);
  const [pending, setPending] = useState(false);
  const disabled = busy || pending;
  useEffect(() => {
    if (task.version > baseline.version && editable(task) === editable(baseline)) setBaseline(task);
  }, [task, baseline]);
  useEffect(() => { if (configure) setWaiting(current => current || newRules()); }, [configure]);
  const stale = task.version !== baseline.version;
  const missingChat = promptRules(waiting).length > 0 && !task.thread_id;
  const readiness = !waiting ? 'Configure waiting to activate it.' : pollingProblem(waiting) || (missingChat ? 'Codex chat required' : promptRules(waiting).some(r => !r.action.prompt?.trim()) ? 'Enter a continuation prompt.' : '');
  const dirty = editable(owned(baseline, config, sourceURL, waiting)) !== editable(baseline);
  const reload = () => { setBaseline(task); setConfig(task.config); setSourceURL(task.source_url); setWaiting(task.polling ? asRules(task.polling, task.config.continuation_prompt) : undefined); setMessage(''); };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (disabled || submitting.current) return;
    if (stale) { setMessage('Reload latest settings before saving. Your draft has been preserved.'); return; }
    const problem = waiting && pollingProblem(waiting);
    if (problem) { setMessage(problem); return; }
    // Missing chat is a readiness condition, so inactive settings can still be saved.
    if (task.column === 'polling' && promptRules(waiting).some(r => !r.action.prompt?.trim())) { setMessage('Enter a continuation prompt.'); return; }
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
  const executionSettings = <details className="form-section"><summary>Execution settings</summary>
    <p className="note">Shared by this task’s prompts.</p>
    <ExecutionFields value={config.execution} onChange={execution => setConfig({ ...config, execution })} />
  </details>;
  return <>
    <form className="workflow-editor" onSubmit={e => void save(e)}>
      {!waiting && <h2>Polling</h2>}
      {waiting ? <PollingRules value={waiting} onChange={setWaiting} promptSettings={executionSettings} /> : <><p className="note">No waiting configured.</p><button type="button" disabled={disabled} onClick={() => setWaiting(newRules())}>Configure waiting</button></>}
      {missingChat && <div className="readiness-banner" role="status"><strong>Codex chat required</strong><p>Prompt rules continue this task’s linked chat.</p><button type="button" disabled={disabled} onClick={() => setShowChat(true)}>Link chat</button></div>}
      <details className="form-section initial-launch"><summary>Initial launch · prompt and source</summary>
        <div className="section-content"><p className="note">Existing launch requests keep their frozen prompt and settings.</p><Field label="Initial prompt"><textarea rows={4} value={config.initial_prompt} onChange={e => setConfig({ ...config, initial_prompt: e.target.value })} /></Field>
          <Field label="Source URL" hint="Task provenance. Waiting uses the PR ID above."><input type="url" value={sourceURL} onChange={e => setSourceURL(e.target.value)} /></Field>
          {!waiting && executionSettings}
          {!waiting && <Field label="Default continuation prompt"><textarea rows={3} value={config.continuation_prompt} onChange={e => setConfig({ ...config, continuation_prompt: e.target.value })} /></Field>}
        </div>
      </details>
      {stale && <div className="readiness-banner"><p>This task changed. Your draft has been preserved.</p><button type="button" disabled={disabled} onClick={reload}>Reload latest settings</button></div>}
      {message && <p className="info-note" role="status">{message}</p>}
      <div className="form-actions workflow-footer"><button className="primary" disabled={disabled}>Save settings</button>
        <button type="button" disabled={disabled || stale || dirty || !!readiness || task.column === 'polling'} title={dirty ? 'Save settings before activating waiting.' : readiness || undefined} onClick={() => void activate()}>Activate waiting</button></div>
      <details className="form-section"><summary>Polling status</summary><PollingDiagnostics task={task} /></details>
    </form>
    {showChat && !task.thread_id && <div className="workflow-chat"><TaskChatForm task={task} busy={disabled} onLink={onLink} inline onCancel={() => setShowChat(false)} submitLabel="Attach chat" /></div>}
  </>;
}
