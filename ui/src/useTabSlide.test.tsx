// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, cleanup } from "@testing-library/react";
import { useRef, useState } from "react";
import { useTabSlide } from "./useTabSlide";
const originalAnimate = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'animate');
afterEach(() => {
  cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals();
  if (originalAnimate) Object.defineProperty(HTMLElement.prototype, 'animate', originalAnimate);
  else delete (HTMLElement.prototype as Partial<HTMLElement>).animate;
});
function Harness() {
  const [tab, setTab] = useState(0), ref = useRef<HTMLDivElement>(null);
  const capture = useTabSlide(ref, tab, 'task', true);
  return <><button onClick={() => { capture(); setTab(value => 1 - value); }}>Switch</button><div ref={ref}>
    <div className="panel-body"><form id="real-form"><div hidden={tab !== 0}><label htmlFor="draft">Draft</label><input id="draft" name="draft" form="real-form" defaultValue="Keep me" /></div><div hidden={tab !== 1}>Polling</div></form></div>
  </div></>;
}
function animations() {
  const result: { onfinish: (() => void) | null; cancel: ReturnType<typeof vi.fn> }[] = [];
  Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, writable: true, value: vi.fn(() => {
    const animation = { onfinish: null, cancel: vi.fn() }; result.push(animation); return animation;
  }) });
  return result;
}
it('keeps the real draft mounted and strips form association from the inert outgoing view', () => {
  const list = animations();
  render(<Harness />);
  const input = screen.getByRole('textbox', { name: 'Draft' });
  fireEvent.change(input, { target: { value: 'Unsaved draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Switch' }));
  const old = document.querySelector('.tab-exit-view')!;
  expect(old.getAttribute('aria-hidden')).toBe('true');
  expect(old.querySelector('[id], [name], [form], [for]')).toBeNull();
  expect(document.querySelectorAll('#real-form')).toHaveLength(1);
  expect(new FormData(document.querySelector<HTMLFormElement>('#real-form')!).getAll('draft')).toEqual(['Unsaved draft']);
  act(() => list.at(-1)!.onfinish!());
  expect(document.querySelector('.tab-exit-view')).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Switch' }));
  expect(screen.getByRole('textbox', { name: 'Draft' })).toBe(input);
  expect((input as HTMLInputElement).value).toBe('Unsaved draft');
});
it('cancels stale transitions on reversal and removes outgoing content on unmount', () => {
  const list = animations(), view = render(<Harness />);
  fireEvent.click(screen.getByRole('button', { name: 'Switch' }));
  const obsolete = list.at(-1)!.onfinish!;
  fireEvent.click(screen.getByRole('button', { name: 'Switch' }));
  act(() => obsolete());
  expect(document.querySelectorAll('.tab-exit-view')).toHaveLength(1);
  expect(screen.getByRole('textbox', { name: 'Draft' })).toBeTruthy();
  view.unmount();
  expect(document.querySelector('.tab-exit-view')).toBeNull();
  expect(list.every(animation => animation.cancel.mock.calls.length)).toBe(true);
});
it('switches immediately without outgoing copies with reduced motion', () => {
  const list = animations();vi.stubGlobal('matchMedia', () => ({ matches: true }));
  render(<Harness />);fireEvent.click(screen.getByRole('button', { name: 'Switch' }));
  expect(document.querySelector('.tab-exit-view')).toBeNull();
  expect(list).toHaveLength(0);
});
