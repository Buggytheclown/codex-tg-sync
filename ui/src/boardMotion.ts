import type { Column } from "./model";

export type DropPosition = { column: Column; beforeID: string };
type Callbacks = {
  start: (id: string, height: number) => boolean;
  over: (element: HTMLElement, y: number, column: Column) => DropPosition;
  target: (position: DropPosition | null) => void;
  drop: (position: DropPosition | null) => void;
};
type Gesture = { id: string; pointer: number; x: number; y: number; dx: number; dy: number; element: HTMLElement; started: boolean };
type Layout = { element: HTMLElement; rect: DOMRect; animation?: Animation };
const reduced = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

// Owns only the visual gesture. App retains ordering, validation and persistence.
export class BoardMotion {
  callbacks!: Callbacks;
  private gesture: Gesture | null = null;
  private ghost: HTMLElement | null = null;
  private target: DropPosition | null = null;
  private landing: { id: string; done: boolean; destination?: DOMRect; animation?: Animation } | null = null;
  private layouts = new Map<string, Layout>();
  private viewportWidth = 0;
  private frame = 0;
  private pulse: { element: HTMLElement; animation: Animation } | null = null;
  private suppressed: { id: string; until: number } | null = null;

  pulseSelection(id: string) {
    this.stopPulse();
    if (reduced() || this.gesture || this.landing) return;
    const button = Array.from(document.querySelectorAll<HTMLElement>(".board [data-task-id]")).find(el => el.dataset.taskId === id);
    const element = button?.closest<HTMLElement>(".card-slot");
    if (!element?.animate) return;
    const animation = element.animate([
      { transform: "translateY(0)" }, { transform: "translateY(-6px)", offset: .35 }, { transform: "translateY(0)" },
    ], { duration: 220, easing: "cubic-bezier(.2,.8,.2,1)" });
    this.pulse = { element, animation };
    animation.onfinish = () => { if (this.pulse?.animation === animation) this.pulse = null; };
  }

  private stopPulse() { this.pulse?.animation.cancel(); this.pulse = null; }

  private unpulsedRect(element: HTMLElement) {
    const rect = element.getBoundingClientRect();
    if (!this.pulse || !this.pulse.element.contains(element)) return rect;
    const transform = new DOMMatrix(getComputedStyle(this.pulse.element).transform);
    return new DOMRect(rect.x - transform.m41, rect.y - transform.m42, rect.width, rect.height);
  }

  pointerDown(event: PointerEvent, id: string, button: HTMLElement, disabled: boolean) {
    if (event.button !== 0 || event.isPrimary === false) return;
    // Touch scrolling stays available over the card; the existing grip starts a touch drag.
    if (event.pointerType === "touch" && !(event.target as Element).closest(".card-grip")) return;
    event.preventDefault(); // Suppress the browser's competing native drag image.
    if (disabled || this.gesture || this.landing) return;
    this.stopPulse();
    button.focus({ preventScroll: true });
    const element = button.closest<HTMLElement>(".card-with-link")!;
    const rect = element.getBoundingClientRect();
    this.gesture = { id, pointer: event.pointerId, x: event.clientX, y: event.clientY,
      dx: event.clientX - rect.left, dy: event.clientY - rect.top, element, started: false };
    window.addEventListener("pointermove", this.pointerMove, { passive: false });
    window.addEventListener("pointerup", this.pointerUp);
    window.addEventListener("pointercancel", this.pointerCancel);
    window.addEventListener("blur", this.pointerCancel);
  }

  private pointerMove = (event: PointerEvent) => {
    const gesture = this.gesture;
    if (!gesture || event.pointerId !== gesture.pointer) return;
    if (!gesture.started && Math.hypot(event.clientX - gesture.x, event.clientY - gesture.y) < 6) return;
    event.preventDefault();
    if (!gesture.element.isConnected) { this.unlisten(); this.gesture = null; return; }
    if (!gesture.started) {
      const rect = gesture.element.getBoundingClientRect();
      if (!this.callbacks.start(gesture.id, rect.height)) { this.unlisten(); this.gesture = null; return; }
      gesture.started = true;
      const ghost = gesture.element.cloneNode(true) as HTMLElement;
      ghost.classList.remove("selected", "grabbed", "saving");
      ghost.classList.add("drag-preview");
      ghost.setAttribute("aria-hidden", "true");
      ghost.inert = true;
      // cloneNode does not preserve a select's live selected value.
      const controls = gesture.element.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("input, textarea, select");
      ghost.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("input, textarea, select").forEach((el, index) => {
        el.value = controls[index].value;
        if (el instanceof HTMLInputElement && controls[index] instanceof HTMLInputElement) el.checked = controls[index].checked;
      });
      for (const el of [ghost, ...ghost.querySelectorAll<HTMLElement>("*")]) {
        el.removeAttribute("id");
        el.removeAttribute("data-task-id");
      }
      Object.assign(ghost.style, { width: `${rect.width}px`, height: `${rect.height}px` });
      document.body.append(ghost);
      document.body.classList.add("board-dragging");
      this.ghost = ghost;
      this.frame = requestAnimationFrame(this.autoScroll);
    }
    gesture.x = event.clientX;
    gesture.y = event.clientY;
    this.positionGhost();
    this.findTarget();
  };

  private positionGhost() {
    if (!this.ghost || !this.gesture) return;
    this.ghost.style.left = `${this.gesture.x - this.gesture.dx}px`;
    this.ghost.style.top = `${this.gesture.y - this.gesture.dy}px`;
  }

  private findTarget() {
    const gesture = this.gesture;
    if (!gesture?.started) return;
    const hit = document.elementFromPoint(gesture.x, gesture.y);
    const done = hit?.closest<HTMLElement>(".done-drop-target");
    const column = hit?.closest<HTMLElement>(".column[data-column]");
    this.target = done ? { column: "done", beforeID: "" }
      : column ? this.callbacks.over(column, gesture.y, column.dataset.column as Column) : null;
    this.callbacks.target(this.target);
  }

  private autoScroll = () => {
    const gesture = this.gesture;
    if (!gesture?.started) return;
    const board = document.querySelector<HTMLElement>(".board");
    if (board) {
      const rect = board.getBoundingClientRect();
      const speed = (point: number, start: number, end: number) => point < start + 40
        ? -Math.min(12, (start + 40 - point) / 3) : point > end - 40 ? Math.min(12, (point - end + 40) / 3) : 0;
      if (gesture.x >= rect.left && gesture.x <= rect.right) {
        if (board.scrollHeight > board.clientHeight && getComputedStyle(board).overflowY !== "visible") {
          board.scrollTop += speed(gesture.y, Math.max(0, rect.top), Math.min(innerHeight, rect.bottom));
        } else if (document.documentElement.scrollHeight > innerHeight) {
          window.scrollBy(0, speed(gesture.y, 0, innerHeight));
        }
        if (board.scrollWidth > board.clientWidth) board.scrollLeft += speed(gesture.x, rect.left, rect.right);
      }
      this.findTarget();
    }
    this.frame = requestAnimationFrame(this.autoScroll);
  };

  private pointerUp = (event: PointerEvent) => {
    if (event.pointerId !== this.gesture?.pointer) return;
    if (this.gesture.started) {
      this.gesture.x = event.clientX;
      this.gesture.y = event.clientY;
      this.findTarget();
    }
    this.release(this.target);
  };
  private pointerCancel = (event: Event) => {
    if ("pointerId" in event && (event as PointerEvent).pointerId !== this.gesture?.pointer) return;
    this.cancel();
  };

  cancel() {
    if (!this.gesture) return false;
    this.release(null);
    return true;
  }

  private release(target: DropPosition | null) {
    const gesture = this.gesture;
    if (!gesture) return;
    this.unlisten();
    this.gesture = null;
    this.target = null;
    if (!gesture.started) return;
    this.suppressed = { id: gesture.id, until: performance.now() + 600 };
    this.landing = { id: gesture.id, done: target?.column === "done" };
    this.callbacks.drop(target);
    // React commits the optimistic destination before measuring the landing.
    this.frame = requestAnimationFrame(() => this.land());
  }

  suppressClick(event: MouseEvent) {
    const id = (event.target as Element).closest<HTMLElement>("[data-task-id]")?.dataset.taskId;
    if (event.detail && this.suppressed && this.suppressed.id === id && performance.now() < this.suppressed.until) {
      this.suppressed = null;
      return true;
    }
    return false;
  }

  // WAAPI moves the visible card; midpoint decisions use its unanimated layout.
  layoutRect(element: HTMLElement): DOMRect {
    const rect = this.unpulsedRect(element);
    const id = element.querySelector<HTMLElement>("[data-task-id]")?.dataset.taskId;
    if (!id || !this.layouts.get(id)?.animation) return rect;
    const transform = new DOMMatrix(getComputedStyle(element).transform);
    return new DOMRect(rect.x - transform.m41, rect.y - transform.m42, rect.width, rect.height);
  }

  layout() {
    if (this.viewportWidth !== window.innerWidth) {
      for (const layout of this.layouts.values()) layout.animation?.cancel();
      this.layouts.clear();
      this.viewportWidth = window.innerWidth;
    }
    const seen = new Set<string>();
    for (const button of document.querySelectorAll<HTMLElement>(".board [data-task-id]")) {
      const id = button.dataset.taskId!;
      const element = button.closest<HTMLElement>(".card-with-link")!;
      seen.add(id);
      const previous = this.layouts.get(id);
      let oldX = previous?.rect.left, oldY = previous?.rect.top;
      if (previous?.animation) {
        if (previous.element.isConnected) {
          const transform = new DOMMatrix(getComputedStyle(previous.element).transform);
          oldX! += transform.m41;
          oldY! += transform.m42;
        }
        previous.animation.cancel();
      }
      const rect = this.unpulsedRect(element);
      const record: Layout = { element, rect };
      this.layouts.set(id, record);
      if (id === this.gesture?.id || id === this.landing?.id) continue;
      if (oldX !== undefined && oldY !== undefined && !reduced() && element.animate &&
          (Math.abs(oldX - rect.left) > 1 || Math.abs(oldY - rect.top) > 1)) {
        record.animation = element.animate([
          { transform: `translate(${oldX - rect.left}px, ${oldY - rect.top}px)` },
          { transform: "translate(0, 0)" },
        ], { duration: 190, easing: "cubic-bezier(.2,.8,.2,1)" });
        record.animation.onfinish = () => { record.animation = undefined; };
      }
    }
    for (const [id, old] of this.layouts) if (!seen.has(id)) { old.animation?.cancel(); this.layouts.delete(id); }
    this.land();
  }

  private land() {
    const landing = this.landing, ghost = this.ghost;
    if (!landing || !ghost) return;
    const button = Array.from(document.querySelectorAll<HTMLElement>(".board [data-task-id]"))
      .find(el => el.dataset.taskId === landing.id);
    const card = button?.closest<HTMLElement>(".card-with-link");
    const destination = card || (landing.done ? document.querySelector<HTMLElement>(".done-toggle") : null);
    if (!destination) { this.finishLanding(); return; }
    const rect = destination.getBoundingClientRect(), previous = landing.destination;
    if (previous && Math.abs(previous.x - rect.x) < 1 && Math.abs(previous.y - rect.y) < 1 &&
        Math.abs(previous.width - rect.width) < 1 && Math.abs(previous.height - rect.height) < 1) return;
    if (card) card.classList.add("drag-landing");
    landing.destination = rect;
    const computed = getComputedStyle(ghost);
    const from = { left: computed.left, top: computed.top, width: computed.width, height: computed.height,
      transform: computed.transform, opacity: computed.opacity };
    landing.animation?.cancel();
    Object.assign(ghost.style, from);
    if (reduced() || !ghost.animate) { this.finishLanding(); return; }
    const to = card
      ? { left: `${rect.left}px`, top: `${rect.top}px`, width: `${rect.width}px`, height: `${rect.height}px`, transform: "scale(1) rotate(0deg)", opacity: 1 }
      : { left: `${rect.left + rect.width / 2 - parseFloat(from.width) / 2}px`, top: `${rect.top + rect.height / 2 - parseFloat(from.height) / 2}px`, transform: "scale(.08) rotate(0deg)", opacity: 0 };
    const frames = card ? [from, { ...to, top: `${rect.top + 2}px`, transform: "scale(.997) rotate(0deg)", offset: .82 }, to] : [from, to];
    const animation = ghost.animate(frames, { duration: 280, easing: "cubic-bezier(.18,.85,.25,1)", fill: "forwards" });
    landing.animation = animation;
    animation.onfinish = () => {
      if (this.landing === landing && landing.animation === animation) this.finishLanding();
    };
  }

  private finishLanding() {
    this.landing?.animation?.cancel();
    this.landing = null;
    this.ghost?.remove();
    this.ghost = null;
    document.body.classList.remove("board-dragging");
    for (const card of document.querySelectorAll<HTMLElement>(".drag-landing")) card.classList.remove("drag-landing");
  }

  private unlisten() {
    cancelAnimationFrame(this.frame);
    window.removeEventListener("pointermove", this.pointerMove);
    window.removeEventListener("pointerup", this.pointerUp);
    window.removeEventListener("pointercancel", this.pointerCancel);
    window.removeEventListener("blur", this.pointerCancel);
  }
  dispose() {
    this.stopPulse();
    this.unlisten();
    this.gesture = null;
    this.finishLanding();
    for (const layout of this.layouts.values()) layout.animation?.cancel();
    this.layouts.clear();
  }
}
