// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { BoardMotion } from "./boardMotion";

afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("ignores hover lift during hit testing and refresh, but animates a real reorder", () => {
  document.body.innerHTML = '<main class="board"><div class="card-with-link"><button data-task-id="a"></button></div></main>';
  const card = document.querySelector<HTMLElement>(".card-with-link")!;
  const board = document.querySelector<HTMLElement>(".board")!;
  let top = 100, lift = 0;
  const rect = () => {
    const y = top + lift - board.scrollTop;
    return { x: 20, y, left: 20, top: y, width: 200, height: 120, right: 220, bottom: y + 120, toJSON() {} };
  };
  vi.spyOn(card, "getBoundingClientRect").mockImplementation(rect);
  const style = getComputedStyle(card);
  vi.spyOn(window, "getComputedStyle").mockImplementation(() => ({ ...style, transform: `matrix(1, 0, 0, 1, 0, ${lift})` }) as CSSStyleDeclaration);
  vi.stubGlobal("DOMMatrix", class { m41 = 0; m42: number; constructor(value: string) { this.m42 = Number(value.slice(7, -1).split(',')[5]); } });
  const cancel = vi.fn();
  const animate = vi.fn(() => ({ cancel, onfinish: null }));
  card.animate = animate as unknown as typeof card.animate;
  const motion = new BoardMotion();
  motion.layout();
  lift = -3;
  expect(motion.layoutRect(card).top).toBe(100);
  motion.layout();
  expect(animate).not.toHaveBeenCalled();
  lift = 0;
  motion.layout();
  expect(animate).not.toHaveBeenCalled();
  board.scrollTop = 40;
  motion.layout();
  expect(animate).not.toHaveBeenCalled();
  top = 250;
  motion.layout();
  expect(animate).toHaveBeenCalledOnce();
  motion.dispose();
  expect(cancel).toHaveBeenCalledOnce();
});
