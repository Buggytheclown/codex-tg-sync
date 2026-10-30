import type { Polling } from './model';
const events = { new_commit: 'Any new commit', all_answered: 'All tracked comments answered', all_resolved: 'All tracked issues resolved', request_changes: 'Request changes', merge_checks_failed: 'Required merge checks failed', pr_merged: 'PR merged' };
export function EventGroups({ config, onChange, allowed = events }: { config: Polling['config']; onChange: (config: Polling['config']) => void; allowed?: Record<string, string> }) {
  const groups = config.condition_groups || (config.conditions || []).map(c => [c]);
  const update = (next: string[][]) => onChange({ ...config, conditions: undefined, condition_groups: next });
  const first = Object.keys(allowed)[0];
  return <div className="event-groups"><span className="field-label">Events</span>
    {groups.map((group, gi) => <div key={gi} className="event-group">
      {gi > 0 && <span className="event-operator">OR</span>}
      <div className="event-terms">{group.map((term, ti) => <div key={ti} className="event-term">
        {ti > 0 && <span className="event-operator">AND</span>}
        <select aria-label={`Event ${gi + 1}.${ti + 1}`} value={term} onChange={e => update(groups.map((g, i) => i === gi ? g.map((t, j) => j === ti ? e.target.value : t) : g))}>
          {Object.entries(allowed).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
        </select>
        <button type="button" aria-label={`Remove event ${gi + 1}.${ti + 1}`} onClick={() => update(groups.map((g, i) => i === gi ? g.filter((_, j) => j !== ti) : g).filter(g => g.length))}>×</button>
      </div>)}<button type="button" className="quiet" onClick={() => update(groups.map((g, i) => i === gi ? [...g, first] : g))}>+ AND</button></div>
    </div>)}
    <button type="button" onClick={() => update([...groups, [first]])}>+ OR event</button>
    <small className="note">AND must be true in the same check. Failed checks exclude Approved and Closed issues.</small>
  </div>;
}
