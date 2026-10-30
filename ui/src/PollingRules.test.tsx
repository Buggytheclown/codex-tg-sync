// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { useState } from 'react';
import { PollingRules } from './PollingRules';
import { newRules, pollingProblem } from './model';
import type { Polling } from './model';
afterEach(cleanup);
function configured(): Polling {
 const p=newRules();p.config.rules![0].config.pr_id='42';p.config.rules![0].action={type:'resume_task',column:'todo',prompt:'Keep my instruction'};return p;
}
function setup(value=configured()) {
 const changed=vi.fn();
 function Editor(){const [p,setP]=useState(value);return <PollingRules value={p} onChange={next=>{setP(next);changed(next)}}/>}
 render(<Editor/>);return changed;
}
function choose(label:string, option:string){fireEvent.click(screen.getByRole('combobox',{name:label}));fireEvent.click(screen.getByRole('option',{name:option}));}
it('shows compact When/Then blocks and opens source and prompt settings separately',()=>{
 const changed=setup();
 expect(screen.getByText('When')).toBeTruthy();expect(screen.getByText('Then')).toBeTruthy();
 expect(screen.queryByLabelText('PR ID')).toBeNull();expect(screen.queryByLabelText('Continuation prompt')).toBeNull();
 const card=document.querySelector('.polling-rule')!;
 const then=within(card.querySelector('.rule-then') as HTMLElement);
 expect(then.getByRole('combobox',{name:'After-event status'}).textContent).toContain('TODO');
 expect(then.getByRole('button',{name:'Prompt action'}).textContent).toContain('ТГ аппрув + промпт');
 fireEvent.click(screen.getByRole('button',{name:'Rule source'}));
 expect((screen.getByLabelText('PR ID') as HTMLInputElement).value).toBe('42');
 fireEvent.change(screen.getByLabelText('Check every · seconds'),{target:{value:'30'}});
 expect(changed.mock.calls.at(-1)![0].config.rules[0].config.interval_seconds).toBe(30);
 fireEvent.click(screen.getByRole('button',{name:'Done'}));
 fireEvent.click(screen.getByRole('button',{name:'Prompt action'}));
 expect(screen.queryByLabelText('PR ID')).toBeNull();
 expect((screen.getByLabelText('Continuation prompt') as HTMLTextAreaElement).value).toBe('Keep my instruction');
});
it('keeps status and launch choices valid and preserves the prompt through mode changes',()=>{
 const changed=setup();fireEvent.click(screen.getByRole('button',{name:'Prompt action'}));
 choose('Prompt launch','Автозапуск + промпт');
 expect(changed.mock.calls.at(-1)![0].config.rules[0].action).toMatchObject({type:'resume_task',column:'todo',auto_start:true,prompt:'Keep my instruction'});
 choose('After-event status','Done');
 expect(changed.mock.calls.at(-1)![0].config.rules[0].action).toMatchObject({type:'return_task',column:'done',auto_start:false,prompt:'Keep my instruction'});
 expect(screen.queryByLabelText('Continuation prompt')).toBeNull();
 choose('Prompt launch','ТГ аппрув + промпт');
 expect(changed.mock.calls.at(-1)![0].config.rules[0].action).toMatchObject({type:'resume_task',column:'todo',auto_start:false,prompt:'Keep my instruction'});
 choose('Prompt launch','No launch');
 expect(pollingProblem(changed.mock.calls.at(-1)![0])).toBe('');
 expect(screen.getByRole('combobox',{name:'After-event status'}).textContent).toContain('Inbox');
});
it('edits AND/OR groups without losing source or action settings',()=>{
 const changed=setup();fireEvent.click(screen.getAllByRole('button',{name:'Rule events'})[0]);
 fireEvent.click(screen.getAllByRole('button',{name:'+ AND'})[0]);
 fireEvent.change(screen.getByLabelText('Event 1.2'),{target:{value:'all_resolved'}});
 fireEvent.click(screen.getByRole('button',{name:'Remove event 2.1'}));
 const p=changed.mock.calls.at(-1)![0];expect(p.config.rules[0]).toMatchObject({config:{pr_id:'42',condition_groups:[['request_changes','all_resolved']]},action:{column:'todo',prompt:'Keep my instruction'}});
 fireEvent.click(screen.getByRole('button',{name:'Done'}));
 expect(screen.getByRole('button',{name:'Rule events'}).textContent).toContain('AND');
});
it('closes only the active settings panel on Escape and restores trigger focus',()=>{
 setup();const bubbled=vi.fn();window.addEventListener('keydown',bubbled);
 const trigger=screen.getByRole('button',{name:'Rule source'});fireEvent.click(trigger);
 fireEvent.keyDown(screen.getByLabelText('PR ID'),{key:'Escape'});
 expect(screen.queryByLabelText('PR ID')).toBeNull();expect(document.activeElement).toBe(trigger);expect(bubbled).not.toHaveBeenCalled();
 fireEvent.click(trigger);fireEvent.keyDown(trigger,{key:'Escape'});expect(trigger.getAttribute('aria-expanded')).toBe('false');expect(bubbled).not.toHaveBeenCalled();
 window.removeEventListener('keydown',bubbled);
});
it('adds a schedule and removes only the selected rule from its settings',()=>{
 const changed=setup();fireEvent.click(screen.getByRole('button',{name:'+ Add rule'}));
 const card=within(document.querySelectorAll('.polling-rule')[1] as HTMLElement);
 fireEvent.click(card.getByRole('combobox',{name:'Event source'}));fireEvent.click(card.getByRole('option',{name:'Cron schedule'}));
 let p=changed.mock.calls.at(-1)![0];expect(p.config.rules.map((r:{type:string})=>r.type)).toEqual(['pr_wait','cron']);
 fireEvent.click(card.getByRole('button',{name:'Done'}));
 fireEvent.click(screen.getByRole('button',{name:'Settings for rule 1'}));
 fireEvent.click(screen.getByRole('button',{name:'Remove rule 1'}));
 p=changed.mock.calls.at(-1)![0];expect(p.config.rules).toHaveLength(1);expect(pollingProblem(p)).toBe('');
});
it('keeps copied source PR placeholders editable in the source block',()=>{
 const p=configured();p.config.rules![0].config.pr_id='{{pr_id}}';
 render(<PollingRules value={p} onChange={vi.fn()} child />);
 expect(screen.getByRole('button',{name:'Rule source'}).textContent).toContain('Source PR');
 fireEvent.click(screen.getByRole('button',{name:'Rule source'}));
 const id=screen.getByLabelText('PR ID') as HTMLInputElement;expect(id.value).toBe('{{pr_id}}');expect(id.readOnly).toBe(false);
});
