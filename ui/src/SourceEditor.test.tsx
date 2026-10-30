// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { SourceEditor } from './SourceEditor';
import { defaultPolling, emptyConfig } from './model';
import type { Polling, Task } from './model';

afterEach(cleanup);
function source(type = 'pr_changes_source'): Task {
  const polling = defaultPolling(type);
  polling.action.child_definition = { title: 'Fix {{pr_title}}', description: 'Keep description', config: { ...emptyConfig(), initial_prompt: 'Fix the feedback' } };
  return { id: 'source', title: 'PR fixes', description: '', column: 'polling', position: 1, thread_id: '', source_url: '', source_key: '', version: 3, config: emptyConfig(), requests: [], created_at: '', updated_at: '', polling };
}
function panel(name: string) {
  const trigger = screen.getAllByRole('button', { name })[0];
  if (trigger.getAttribute('aria-expanded') !== 'true') fireEvent.click(trigger);
}
function choose(label: string, option: string) {
  fireEvent.click(screen.getByRole('combobox', { name: label }));
  fireEvent.click(screen.getByRole('option', { name: option }));
}
it('separates Arcanum PR, explicit events, template, status and prompt controls', async () => {
  const t = source(); t.polling!.config.check_merge_failures = true;
  const save = vi.fn(async (polling: Polling) => ({ ...t, polling, version: 4 }));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  expect(screen.queryByText('Workflow steps')).toBeNull();
  expect(screen.getByRole('button', { name: 'Source settings' }).textContent).toContain('Arcanum PR');
  expect(screen.getAllByRole('button', { name: 'Source events' }).map(b => b.textContent)).toEqual(['Request changes', 'Merge checks failed']);
  expect(screen.queryByLabelText('Initial prompt')).toBeNull();
  panel('Source settings');
  fireEvent.change(screen.getByLabelText('Check every · seconds'), { target: { value: '30' } });
  panel('Source events');
  fireEvent.click(screen.getAllByRole('button', { name: '+ AND' })[0]);
  fireEvent.change(screen.getByLabelText('Event 1.2'), { target: { value: 'merge_checks_failed' } });
  fireEvent.click(screen.getByRole('button', { name: 'Remove event 2.1' }));
  panel('Task template');
  fireEvent.change(screen.getByLabelText('Task title'), { target: { value: 'Updated {{pr_title}}' } });
  panel('Initial prompt action');
  const launch = screen.getByRole('region', { name: 'Initial prompt action' });
  expect(within(launch).getByText('Execution settings')).toBeTruthy();
  expect(screen.queryByText('Permissions')).toBeNull();
  fireEvent.change(screen.getByLabelText('Initial prompt'), { target: { value: 'Updated instruction' } });
  fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'chosen-model' } });
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][0]).toMatchObject({ type: 'pr_changes_source', config: { interval_seconds: 30, condition_groups: [['request_changes', 'merge_checks_failed']] }, action: { column: 'todo', initial_launch: { mode: 'manual' }, child_definition: { title: 'Updated {{pr_title}}', description: 'Keep description', config: { initial_prompt: 'Updated instruction', execution: { model: 'chosen-model', approval_policy: 'on-request' } } } } });
  expect(save.mock.calls[0][0].config.check_merge_failures).toBeUndefined();
});
it('keeps destinations and launch modes valid while retaining drafts', async () => {
  const t = source(); const save = vi.fn(async (polling: Polling) => ({ ...t, polling, version: 4 }));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  panel('Initial prompt action');
  choose('Initial launch', 'Automatic · trusted PR authors');
  fireEvent.change(screen.getByLabelText('Trusted PR authors'), { target: { value: 'alice, bob' } });
  choose('New task status', 'Done');
  expect(screen.queryByLabelText('Initial prompt')).toBeNull();
  choose('Initial launch', 'ТГ аппрув + промпт');
  expect((screen.getByLabelText('Initial prompt') as HTMLTextAreaElement).value).toBe('Fix the feedback');
  expect(screen.getByRole('combobox', { name: 'New task status' }).textContent).toContain('TODO');
  choose('Initial launch', 'No launch');
  expect(screen.getByRole('combobox', { name: 'New task status' }).textContent).toContain('Inbox');
  choose('New task status', 'Done');
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][0].action).toMatchObject({ column: 'done', initial_launch: { mode: 'none' }, child_definition: { config: { initial_prompt: 'Fix the feedback' } } });
});
it('preserves the exact legacy feedback setting until event edits', async () => {
  const t = source(); const save = vi.fn(async (polling: Polling) => ({ ...t, polling, version: 4 }));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  expect(screen.getAllByRole('button', { name: 'Source events' }).map(b => b.textContent)).toEqual(['Request changes']);
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][0].config).toEqual(t.polling!.config);
});
it('selects PR scope separately and preserves the template when switching to Cron', async () => {
  const t = source('pr_review_source');
  t.polling!.action.initial_launch = { mode: 'trusted_authors', authors: ['alice'] };
  const save = vi.fn(async (polling: Polling) => ({ ...t, polling, version: 4 }));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  expect(screen.getByRole('button', { name: 'Source events' }).textContent).toBe('Assigned as reviewer');
  panel('Source settings');
  choose('PR selection', 'PRs I authored');
  expect(screen.getByRole('button', { name: 'Source events' }).textContent).toBe('Request changes');
  choose('Event source', 'Cron schedule');
  fireEvent.change(screen.getByLabelText('Cron schedule'), { target: { value: '0 10 * * 2' } });
  expect(screen.getByRole('button', { name: 'Source events' }).textContent).toContain('Scheduled time');
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][0]).toMatchObject({ type: 'cron', config: { cron: '0 10 * * 2' }, action: { initial_launch: { mode: 'manual' }, child_definition: { title: 'Fix {{pr_title}}', config: { initial_prompt: 'Fix the feedback' } } } });
});
it('closes the source menu and settings locally, restoring trigger focus', () => {
  render(<SourceEditor task={source()} busy={false} onSave={vi.fn()} />);
  const bubbled = vi.fn(); window.addEventListener('keydown', bubbled);
  const trigger = screen.getByRole('button', { name: 'Source settings' }); panel('Source settings');
  const select = screen.getByRole('combobox', { name: 'Event source' });
  fireEvent.click(select); fireEvent.keyDown(select, { key: 'Escape' });
  expect(screen.queryByLabelText('Check every · seconds')).toBeTruthy();
  fireEvent.keyDown(select, { key: 'Escape' });
  expect(screen.queryByLabelText('Check every · seconds')).toBeNull();
  expect(document.activeElement).toBe(trigger); expect(bubbled).not.toHaveBeenCalled();
  window.removeEventListener('keydown', bubbled);
});
it('opens missing template and prompt fields when saving a new source', () => {
  const save = vi.fn(); render(<SourceEditor busy={false} onSave={save} />);
  fireEvent.change(screen.getByLabelText('Source name'), { target: { value: 'Reviews' } });
  fireEvent.submit(document.querySelector('form')!);
  expect(screen.getByLabelText('Task title')).toBeTruthy();
  fireEvent.change(screen.getByLabelText('Task title'), { target: { value: 'Review {{pr_id}}' } });
  fireEvent.submit(document.querySelector('form')!);
  expect(screen.getByLabelText('Initial prompt')).toBeTruthy();
  expect(save).not.toHaveBeenCalled();
});

it('preserves a failed source draft across panel switches and retries only on save', async () => {
  const t = source();
  const save = vi.fn(async (polling: Polling) => ({ ...t, polling, version: 4 }));
  save.mockRejectedValueOnce(new Error('Temporary failure'));
  render(<SourceEditor task={t} busy={false} onSave={save} />);
  panel('Initial prompt action');
  fireEvent.change(screen.getByLabelText('Initial prompt'), { target: { value: 'Keep failed draft' } });
  expect(save).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await screen.findByText('Temporary failure');
  panel('Task template'); panel('Initial prompt action');
  expect((screen.getByLabelText('Initial prompt') as HTMLTextAreaElement).value).toBe('Keep failed draft');
  fireEvent.click(screen.getByRole('button', { name: 'Save source' }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][0].action.child_definition!.config.initial_prompt).toBe('Keep failed draft');
});
