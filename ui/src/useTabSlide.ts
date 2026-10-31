import { useCallback, useLayoutEffect, useRef } from "react";
import type { RefObject } from "react";

// Only the outgoing picture is copied. Real editors keep their existing lifecycle.
export function useTabSlide(ref: RefObject<HTMLElement | null>, index: number, identity: string, visible: boolean) {
  const previous = useRef({ index, identity });
  const pending = useRef<{ view: HTMLElement; height: number; opacity: string; transform: string } | null>(null);
  const running = useRef<{ stage: HTMLElement; view: HTMLElement; animations: Animation[] } | null>(null);
  const generation = useRef(0);
  const stop = useCallback(() => {
    ++generation.current;
    const active = running.current;
    if (active) {
      active.animations.forEach(animation => { animation.onfinish = null; animation.cancel(); });
      active.view.remove();active.stage.style.height = "";active.stage.classList.remove("is-sliding");
      running.current = null;
    }
  }, []);
  const capture = useCallback(() => {
    const stage = ref.current, live = stage?.firstElementChild as HTMLElement | null;
    pending.current = null;
    if (!stage || !live || !live.animate || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) { stop(); return; }
    const computed = getComputedStyle(live);
    const opacity = computed.opacity || "1", transform = computed.transform || "none", height = stage.getBoundingClientRect().height;
    stop();
    const view = live.cloneNode(true) as HTMLElement;
    const controls = live.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("input, textarea, select");
    view.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("input, textarea, select").forEach((el, i) => {
      el.value = controls[i].value;
      if (el instanceof HTMLInputElement && controls[i] instanceof HTMLInputElement) el.checked = controls[i].checked;
    });
    for (const el of [view, ...view.querySelectorAll<HTMLElement>("*")]) {
      for (const attr of ["id", "name", "form", "for"]) el.removeAttribute(attr);
    }
    view.classList.add("tab-exit-view");view.setAttribute("aria-hidden", "true");view.inert = true;
    pending.current = { view, height, opacity, transform };
  }, [ref, stop]);
  useLayoutEffect(() => {
    const last = previous.current;
    previous.current = { index, identity };
    const old = pending.current;pending.current = null;
    stop();
    const stage = ref.current, live = stage?.firstElementChild as HTMLElement | null;
    if (!old || !visible || identity !== last.identity || index === last.index || !stage || !live || !live.animate || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
    const direction = index > last.index ? 1 : -1;
    const height = live.getBoundingClientRect().height, current = generation.current;
    stage.classList.add("is-sliding");stage.append(old.view);stage.style.height = `${old.height}px`;
    const options: KeyframeAnimationOptions = { duration: 210, easing: "cubic-bezier(.2,.8,.2,1)", fill: "both" };
    const size = stage.animate([{ height: `${old.height}px` }, { height: `${height}px` }], options);
    const exit = old.view.animate([{ opacity: old.opacity, transform: old.transform }, { opacity: 0, transform: `translateX(${-direction * 24}px)` }], options);
    const enter = live.animate([{ opacity: 0, transform: `translateX(${direction * 24}px)` }, { opacity: 1, transform: "none" }], options);
    running.current = { stage, view: old.view, animations: [size, exit, enter] };
    enter.onfinish = () => { if (generation.current === current) stop(); };
    return stop;
  }, [ref, index, identity, visible, stop]);
  return capture;
}
