import type { Polling, PollingRule } from './model';
import { defaultRule } from './model';
import { Field } from './forms';
import { ChipSelect } from './ChipSelect';
import { EventGroups } from './EventGroups';

export function RuleAction({ value, onChange }: { value: PollingRule; onChange: (r: PollingRule) => void }) {
  const action = value.action;
  const selected = action.type === 'resume_task' ? action.auto_start ? 'auto' : 'manual' : 'move';
  return <div className="rule-after"><h4>After event</h4>
    <ChipSelect label="After event" value={selected} options={[
      { value: 'move', label: 'Move task · no model launch' },
      { value: 'manual', label: 'TODO + prompt · Telegram approval' },
      { value: 'auto', label: 'TODO + prompt · automatic' },
    ]} onChange={kind => onChange({ ...value, action: kind === 'move' ? { type: 'return_task', column: 'inbox' } : { type: 'resume_task', column: 'todo', auto_start: kind === 'auto', prompt: action.prompt || '' } })} />
    {selected === 'move' ? <Field label="Move to"><select value={action.column || 'inbox'} onChange={e => onChange({ ...value, action: { ...action, column: e.target.value as 'inbox' | 'done' } })}><option value="inbox">Inbox</option><option value="done">Done</option></select></Field>
      : <><p className="note">Task moves to TODO and all its rules stop. {action.auto_start ? 'The prompt starts automatically in the linked chat when it is available.' : 'Telegram Start launches the prompt in the linked chat.'}</p><Field label="Continuation prompt"><textarea rows={3} value={action.prompt || ''} onChange={e => onChange({ ...value, action: { ...action, prompt: e.target.value } })} /></Field></>}
  </div>;
}
export function PollingRules({ value, onChange, child = false, variables = true }: { value: Polling; onChange: (p: Polling) => void; child?: boolean; variables?: boolean }) {
  const rules = value.config.rules || [];
  const update = (next: PollingRule[]) => onChange({ ...value, config: { rules: next } });
  const replace = (i: number, r: PollingRule) => update(rules.map((old, j) => j === i ? r : old));
  return <div className="polling-rules">
    <p className="note">Independent rules. Any action leaves Polling and stops every rule. Done wins simultaneous matches; otherwise the first matching rule wins.</p>
    {rules.map((rule, i) => <details className="polling-rule form-section" key={rule.id} open={i === 0 || undefined}>
      <summary><strong>{rule.title || `Rule ${i + 1}`}</strong><span className="note">{rule.type === 'cron' ? 'Schedule' : 'Arcanum PR'} → {rule.action.column === 'done' ? 'Done' : rule.action.type === 'resume_task' ? 'TODO + prompt' : 'Inbox'}</span></summary>
      <div className="section-content">
        <div className="rule-heading"><Field label="Rule name"><input value={rule.title || ''} placeholder={`Rule ${i + 1}`} onChange={e => replace(i, { ...rule, title: e.target.value })} /></Field><button type="button" aria-label={`Remove rule ${i + 1}`} onClick={() => update(rules.filter((_, j) => j !== i))}>Remove</button></div>
        <h4>Wait</h4>
        <ChipSelect label="Event source" value={rule.type} options={[{ value: 'pr_wait', label: 'Arcanum PR' }, { value: 'cron', label: 'Cron schedule' }]} onChange={type => replace(i, { ...rule, type, config: type === 'cron' ? { cron: '0 9 * * 1', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, max_lateness: '5m' } : defaultRule().config })} />
        {rule.type === 'cron' ? <>
          <Field label="Cron schedule" hint="Daily or one weekday (0–6), for example 0 9 * * 1."><input value={rule.config.cron || ''} onChange={e => replace(i, { ...rule, config: { ...rule.config, cron: e.target.value } })} /></Field>
          <Field label="Timezone"><input value={rule.config.timezone || ''} onChange={e => replace(i, { ...rule, config: { ...rule.config, timezone: e.target.value } })} /></Field>
          <Field label="Maximum lateness"><input value={rule.config.max_lateness || ''} onChange={e => replace(i, { ...rule, config: { ...rule.config, max_lateness: e.target.value } })} /></Field>
          <p className="note">Event: scheduled time reached.</p>
        </> : <>
          <div className="pr-id-row"><Field label="PR ID" hint={child && variables ? 'Use {{pr_id}} for the source PR, or enter a numeric PR ID.' : undefined}><input required pattern={child ? undefined : "[1-9][0-9]*"} value={rule.config.pr_id || ''} onChange={e => replace(i, { ...rule, config: { ...rule.config, pr_id: e.target.value } })} /></Field>
            {/^[1-9]\d*$/.test(rule.config.pr_id || '') && <a className="pr-target-link" href={`https://a.yandex-team.ru/review/${rule.config.pr_id}`} target="_blank" rel="noreferrer" aria-label={`#${rule.config.pr_id} Open PR`}>#{rule.config.pr_id} Open PR ↗</a>}</div>
          <details className="polling-author"><summary>PR author (optional)</summary><Field label="PR author"><input value={rule.config.author || ''} onChange={e => replace(i, { ...rule, config: { ...rule.config, author: e.target.value } })} /></Field></details>
          <EventGroups config={rule.config} onChange={config => replace(i, { ...rule, config })} />
          <Field label="Check every · seconds"><input type="number" min={10} max={86400} value={rule.config.interval_seconds || 60} onChange={e => replace(i, { ...rule, config: { ...rule.config, interval_seconds: Number(e.target.value) } })} /></Field>
        </>}
        <RuleAction value={rule} onChange={next => replace(i, next)} />
      </div>
    </details>)}
    <button type="button" disabled={rules.length >= 20} onClick={() => update([...rules, defaultRule()])}>+ Add rule</button>
  </div>;
}
