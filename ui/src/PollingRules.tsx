import { useId, useRef, useState } from 'react';
import type { MouseEvent, ReactNode } from 'react';
import type { Polling, PollingRule } from './model';
import { defaultRule } from './model';
import { Field } from './forms';
import { ChipSelect } from './ChipSelect';
import { EventGroups, EventSummary } from './EventGroups';

type Panel = 'source' | 'events' | 'run' | 'settings';
const launchOptions = [
  { value: 'none', label: 'No launch' },
  { value: 'manual', label: 'ТГ аппрув + промпт' },
  { value: 'auto', label: 'Автозапуск + промпт' },
] as const;
function scheduleLabel(config: Polling['config']) {
  const parts = (config.cron || '').trim().split(/\s+/);
  if (parts.length === 5 && /^\d+$/.test(parts[0]) && /^\d+$/.test(parts[1]) && parts[2] === '*' && parts[3] === '*') {
    const day = parts[4] === '*' ? 'Daily' : ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][Number(parts[4])];
    if (day) return `${day} · ${parts[1].padStart(2, '0')}:${parts[0].padStart(2, '0')}`;
  }
  return config.cron || 'Configure schedule';
}
function RuleCard({ rule, index, onChange, onRemove, child, variables, promptSettings }: {
  rule: PollingRule; index: number; onChange: (r: PollingRule) => void; onRemove: () => void; child: boolean; variables: boolean; promptSettings?: ReactNode;
}) {
  const [panel, setPanel] = useState<Panel | null>(() => rule.type === 'pr_wait' && !rule.config.pr_id ? 'source' : null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const editorID = useId();
  const toggle = (next: Panel, event: MouseEvent<HTMLButtonElement>) => {
    trigger.current = event.currentTarget;
    setPanel(current => current === next ? null : next);
  };
  const close = () => { setPanel(null); trigger.current?.focus(); };
  const cfg = (key: keyof Polling['config'], value: string | number) => onChange({ ...rule, config: { ...rule.config, [key]: value } });
  const action = rule.action;
  const launch = action.type === 'resume_task' ? action.auto_start ? 'auto' : 'manual' : 'none';
  const column = action.type === 'resume_task' ? 'todo' : action.column === 'done' ? 'done' : 'inbox';
  const setColumn = (next: 'inbox' | 'todo' | 'done') => onChange({ ...rule, action: {
    ...action, type: next === 'todo' ? 'resume_task' : 'return_task', column: next,
    auto_start: next === 'todo' && action.auto_start === true,
  } });
  const setLaunch = (next: typeof launch) => onChange({ ...rule, action: {
    ...action, type: next === 'none' ? 'return_task' : 'resume_task',
    column: next === 'none' ? column === 'done' ? 'done' : 'inbox' : 'todo', auto_start: next === 'auto',
  } });
  const groups = rule.config.condition_groups || (rule.config.conditions || []).map(term => [term]);
  const source = rule.type === 'cron' ? 'Cron' : rule.config.pr_id === '{{pr_id}}' ? 'Source PR'
    : rule.config.pr_id ? `PR #${rule.config.pr_id}` : 'Configure PR';
  const title = { source: 'Source and check frequency', events: 'Events', run: 'Prompt action', settings: 'Rule settings' };
  return <section className="polling-rule" aria-label={rule.title || `Rule ${index + 1}`} onKeyDown={event => {
    if (panel && event.key === 'Escape' && !event.nativeEvent.isComposing) { event.preventDefault(); event.stopPropagation(); close(); }
  }}>
    <div className="rule-card-heading"><strong>{rule.title || `Rule ${index + 1}`}</strong>
      <button type="button" className="rule-settings-button" aria-label={`Settings for rule ${index + 1}`} aria-expanded={panel === 'settings'} aria-controls={panel === 'settings' ? editorID : undefined} onClick={e => toggle('settings', e)}>⋯</button>
    </div>
    <div className="rule-card-body">
      <div className="rule-card-row"><span className="rule-row-label">When</span><div className="rule-blocks">
        <button type="button" className="rule-block" aria-label="Rule source" aria-expanded={panel === 'source'} aria-controls={panel === 'source' ? editorID : undefined} onClick={e => toggle('source', e)}>{source}<span className="rule-chevron" aria-hidden="true">⌄</span></button>
        {rule.type === 'cron' ? <button type="button" className="rule-block" aria-label="Schedule settings" title={`${rule.config.cron || ''} · ${rule.config.timezone || ''}`} onClick={e => toggle('source', e)}>{scheduleLabel(rule.config)}</button>
          : <EventSummary groups={groups} expanded={panel === 'events'} controls={editorID} onClick={e => toggle('events', e)} />}
      </div></div>
      <div className="rule-card-row rule-then"><span className="rule-row-label">Then</span><div className="rule-blocks">
        <ChipSelect label="After-event status" value={column} options={[{ value: 'inbox', label: 'Inbox' }, { value: 'todo', label: 'TODO' }, { value: 'done', label: 'Done' }]} onChange={setColumn} className="rule-target" />
        <button type="button" className="rule-block" aria-label="Prompt action" aria-expanded={panel === 'run'} aria-controls={panel === 'run' ? editorID : undefined} onClick={e => toggle('run', e)}>{launchOptions.find(option => option.value === launch)!.label}<span className="rule-chevron" aria-hidden="true">⌄</span></button>
      </div></div>
    </div>
    {panel && <div id={editorID} className="rule-editor" role="region" aria-label={title[panel]}>
      <div className="rule-editor-heading"><strong>{title[panel]}</strong><button type="button" onClick={close}>Done</button></div>
      {panel === 'settings' && <>
        <Field label="Rule name"><input value={rule.title || ''} placeholder={`Rule ${index + 1}`} onChange={e => onChange({ ...rule, title: e.target.value })} /></Field>
        <button type="button" aria-label={`Remove rule ${index + 1}`} onClick={onRemove}>Remove rule</button>
      </>}
      {panel === 'source' && <>
        <ChipSelect label="Event source" value={rule.type} options={[{ value: 'pr_wait', label: 'Arcanum PR' }, { value: 'cron', label: 'Cron schedule' }]} onChange={type => onChange({ ...rule, type, config: type === 'cron'
          ? { cron: '0 9 * * 1', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, max_lateness: '5m' } : defaultRule().config })} />
        {rule.type === 'cron' ? <>
          <Field label="Cron schedule" hint="Daily or one weekday (0–6), for example 0 9 * * 1."><input value={rule.config.cron || ''} onChange={e => cfg('cron', e.target.value)} /></Field>
          <Field label="Timezone"><input value={rule.config.timezone || ''} onChange={e => cfg('timezone', e.target.value)} /></Field>
          <Field label="Maximum lateness"><input value={rule.config.max_lateness || ''} onChange={e => cfg('max_lateness', e.target.value)} /></Field>
        </> : <>
          <div className="pr-id-row"><Field label="PR ID" hint={child && variables ? 'Use {{pr_id}} for the source PR, or enter a numeric PR ID.' : undefined}><input required pattern={child ? undefined : '[1-9][0-9]*'} value={rule.config.pr_id || ''} onChange={e => cfg('pr_id', e.target.value)} /></Field>
            {/^[1-9]\d*$/.test(rule.config.pr_id || '') && <a className="pr-target-link" href={`https://a.yandex-team.ru/review/${rule.config.pr_id}`} target="_blank" rel="noreferrer" aria-label={`#${rule.config.pr_id} Open PR`}>#{rule.config.pr_id} Open PR ↗</a>}</div>
          <Field label="Check every · seconds"><input type="number" min={10} max={86400} value={rule.config.interval_seconds || 60} onChange={e => cfg('interval_seconds', Number(e.target.value))} /></Field>
          <details className="polling-author"><summary>PR author (optional)</summary><Field label="PR author"><input value={rule.config.author || ''} onChange={e => cfg('author', e.target.value)} /></Field></details>
        </>}
      </>}
      {panel === 'events' && <EventGroups config={rule.config} onChange={config => onChange({ ...rule, config })} />}
      {panel === 'run' && <>
        <ChipSelect label="Prompt launch" value={launch} options={launchOptions} onChange={setLaunch} />
        {launch !== 'none' && <>
          <Field label="Continuation prompt"><textarea rows={3} value={action.prompt || ''} onChange={e => onChange({ ...rule, action: { ...action, prompt: e.target.value } })} /></Field>
          {promptSettings}
          <p className="note">{launch === 'auto' ? 'Starts automatically in the linked chat when it is idle.' : 'Starts in the linked chat after Telegram approval.'} The task moves to TODO.</p>
        </>}
      </>}
    </div>}
  </section>;
}
export function PollingRules({ value, onChange, child = false, variables = true, promptSettings }: { value: Polling; onChange: (p: Polling) => void; child?: boolean; variables?: boolean; promptSettings?: ReactNode }) {
  const rules = value.config.rules || [];
  const update = (next: PollingRule[]) => onChange({ ...value, config: { ...value.config, rules: next } });
  return <div className="polling-rules">
    <div className="polling-rules-heading"><h3>Rules <span>{rules.length}</span></h3><button type="button" disabled={rules.length >= 20} onClick={() => update([...rules, defaultRule()])}>+ Add rule</button></div>
    {rules.map((rule, i) => <RuleCard key={rule.id} rule={rule} index={i} child={child} variables={variables} promptSettings={promptSettings}
      onChange={next => update(rules.map((old, j) => j === i ? next : old))} onRemove={() => update(rules.filter((_, j) => j !== i))} />)}
    {!child && <p className="note rule-stop-note">Any action leaves Polling and stops all task rules. Done wins simultaneous matches; otherwise rule order applies.</p>}
  </div>;
}
