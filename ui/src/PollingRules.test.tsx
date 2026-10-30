// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { useState } from 'react';
import { PollingRules } from './PollingRules';
import { newRules, pollingProblem } from './model';
afterEach(cleanup);
it('edits AND/OR groups and automatic TODO action independently', () => {
 const changed=vi.fn();
 function Editor(){const [p,setP]=useState(newRules);return <PollingRules value={p} onChange={value=>{setP(value);changed(value)}}/>}
 render(<Editor/>);
 fireEvent.click(screen.getAllByRole('button',{name:'+ AND'})[0]);
 fireEvent.change(screen.getByLabelText('Event 1.2'),{target:{value:'merge_checks_failed'}});
 fireEvent.click(screen.getByRole('button',{name:'Remove event 2.1'}));
 fireEvent.click(screen.getByRole('combobox',{name:'After event'}));
 fireEvent.click(screen.getByRole('option',{name:'TODO + prompt · automatic'}));
 fireEvent.change(screen.getByLabelText('Continuation prompt'),{target:{value:'Fix feedback'}});
 const p=changed.mock.calls.at(-1)![0];
 expect(p.config.rules[0]).toMatchObject({config:{condition_groups:[['request_changes','merge_checks_failed']]},action:{type:'resume_task',column:'todo',auto_start:true,prompt:'Fix feedback'}});
 expect(screen.queryByText('Approval')).toBeNull();
});
it('adds a schedule alongside a PR and removes only the selected rule', () => {
 const changed=vi.fn();function Editor(){const [p,setP]=useState(newRules);return <PollingRules value={p} onChange={value=>{setP(value);changed(value)}}/>}
 render(<Editor/>);fireEvent.click(screen.getByRole('button',{name:'+ Add rule'}));
 const card=within(document.querySelectorAll('.polling-rule')[1] as HTMLElement);
 fireEvent.click(card.getByRole('combobox',{name:'Event source'}));fireEvent.click(card.getByRole('option',{name:'Cron schedule'}));
 let p=changed.mock.calls.at(-1)![0];expect(p.config.rules.map((r:{type:string})=>r.type)).toEqual(['pr_wait','cron']);
 fireEvent.click(screen.getByRole('button',{name:'Remove rule 1'}));p=changed.mock.calls.at(-1)![0];expect(p.config.rules).toHaveLength(1);expect(pollingProblem(p)).toBe('');
});
