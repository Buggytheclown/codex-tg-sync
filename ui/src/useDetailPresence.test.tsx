// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { useDetailPresence } from "./useDetailPresence";

function Harness({ visible, identity = "a" }: { visible: boolean; identity?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const present = useDetailPresence(ref, visible, identity);
  return <>{!present && <p>Empty</p>}<div ref={ref} hidden={!present} aria-hidden={!visible} inert={!visible}><input aria-label="Draft" defaultValue="Keep me" /></div></>;
}
const originalAnimate = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "animate");
afterEach(() => {
  cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks();
  if (originalAnimate) Object.defineProperty(HTMLElement.prototype, "animate", originalAnimate);
  else delete (HTMLElement.prototype as Partial<HTMLElement>).animate;
});
function animations() {
  const list: { onfinish: (() => void) | null; cancel: ReturnType<typeof vi.fn> }[] = [];
  vi.spyOn(HTMLElement.prototype, "animate").mockImplementation(() => {
    const animation = { onfinish: null, cancel: vi.fn() };
    list.push(animation);
    return animation as unknown as Animation;
  });
  return list;
}
it("keeps the editor mounted through exit while removing its input from the accessible UI", () => {
  Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, writable: true, value() {} });
  const list = animations();
  const view = render(<Harness visible />);
  const input = screen.getByRole('textbox', { name: 'Draft' });
  view.rerender(<Harness visible={false} />);
  expect(screen.queryByRole('textbox')).toBeNull();
  expect(input.isConnected).toBe(true);
  expect(screen.queryByText('Empty')).toBeNull();
  act(() => list.at(-1)!.onfinish!());
  expect(screen.getByText('Empty')).toBeTruthy();
  view.rerender(<Harness visible />);
  expect(screen.getByRole('textbox', { name: 'Draft' })).toBe(input);
});
it("cannot let an obsolete closing animation hide a reopened or different task", () => {
  Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, writable: true, value() {} });
  const list = animations();
  const view = render(<Harness visible />);
  view.rerender(<Harness visible={false} />);
  const closing = list.at(-1)!;
  const obsoleteFinish = closing.onfinish!;
  view.rerender(<Harness visible identity="b" />);
  act(() => obsoleteFinish());
  expect(screen.getByRole('textbox', { name: 'Draft' })).toBeTruthy();
  expect(screen.queryByText('Empty')).toBeNull();
  expect(closing.cancel).toHaveBeenCalled();
});
it("hides immediately with reduced motion and cancels animation on unmount", () => {
  Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, writable: true, value() {} });
  const list = animations();
  const view = render(<Harness visible />);
  view.unmount();
  expect(list[0].cancel).toHaveBeenCalled();
  vi.stubGlobal('matchMedia', () => ({ matches: true }));
  const reduced = render(<Harness visible />);
  reduced.rerender(<Harness visible={false} />);
  expect(screen.getByText('Empty')).toBeTruthy();
});
