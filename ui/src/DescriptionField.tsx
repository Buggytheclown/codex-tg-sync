import { useId, useLayoutEffect, useRef } from "react";

export function DescriptionField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const id = useId();
  const editor = useRef<HTMLTextAreaElement>(null);
  useLayoutEffect(() => {
    const node = editor.current!;
    const measure = () => {
      // Hidden Overview keeps its last size until its tab becomes visible again.
      if (!node.getClientRects().length) return;
      node.style.height = "auto";
      node.style.height = `${node.scrollHeight + 2}px`;
    };
    measure();
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(measure) : undefined;
    observer?.observe(node);
    window.addEventListener("resize", measure);
    return () => { observer?.disconnect(); window.removeEventListener("resize", measure); };
  }, [value]);
  return <div className="field overview-description">
    <label htmlFor={id}>Description</label>
    <textarea id={id} ref={editor} rows={3} value={value} onChange={e => onChange(e.target.value)} />
  </div>;
}
