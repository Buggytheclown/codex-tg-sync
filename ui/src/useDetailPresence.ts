import { useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";

// Presentation presence is separate from selection: closed controls become inert
// immediately, while their mounted editor finishes a short fade and keeps its draft.
export function useDetailPresence(ref: RefObject<HTMLElement | null>, visible: boolean, identity: string) {
  const [present, setPresent] = useState(visible);
  const generation = useRef(0);
  const lastIdentity = useRef(identity);
  const visual = useRef<{ opacity: string; transform: string } | null>(null);
  useLayoutEffect(() => {
    const current = ++generation.current;
    if (visible && !present) { visual.current = null; setPresent(true); return; }
    const node = ref.current;
    if (!present || !node) return;
    const changed = lastIdentity.current !== identity;
    lastIdentity.current = identity;
    if (visible && changed && node.parentElement) node.parentElement.scrollTop = 0;
    if (!node.animate || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      if (!visible) setPresent(false);
      return;
    }
    const from = visible && changed ? { opacity: "0", transform: "translateY(-12px)" }
      : visual.current || { opacity: visible ? "0" : "1", transform: visible ? "translateY(-12px)" : "none" };
    const to = { opacity: visible ? "1" : "0", transform: visible ? "none" : "translateY(-10px)" };
    const animation = node.animate([from, to], {
      duration: visible ? 220 : 140, easing: "cubic-bezier(.2,.8,.2,1)", fill: "both",
    });
    animation.onfinish = () => {
      if (generation.current !== current) return;
      if (!visible) setPresent(false);
      else { visual.current = to; animation.cancel(); }
    };
    return () => {
      ++generation.current;
      const style = getComputedStyle(node);
      visual.current = { opacity: style.opacity || "1", transform: style.transform || "none" };
      animation.onfinish = null;
      animation.cancel();
    };
  }, [ref, visible, present, identity]);
  return present;
}
