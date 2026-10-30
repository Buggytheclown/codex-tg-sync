import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";

type Option<T extends string> = { value: T; label: string; icon?: string };

// A select-only combobox: focus stays on its trigger while the list is open.
export function ChipSelect<T extends string>({ label, value, options, onChange, onBlur, disabled, form, align = "start", className = "" }: {
  label: string; value: T; options: readonly Option<T>[];
  onChange: (value: T) => void; onBlur?: () => void;
  disabled?: boolean; form?: string; align?: "start" | "end"; className?: string;
}) {
  const id = useId();
  const root = useRef<HTMLDivElement>(null), trigger = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const selected = Math.max(0, options.findIndex(option => option.value === value));
  const [active, setActive] = useState(selected);
  const expanded = open && !disabled;
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);
  useEffect(() => {
    if (!expanded) return;
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false);
    };
    window.addEventListener("pointerdown", outside, true);
    return () => window.removeEventListener("pointerdown", outside, true);
  }, [expanded]);
  const choose = (index: number) => {
    setOpen(false);
    trigger.current?.focus();
    if (options[index].value !== value) onChange(options[index].value);
  };
  const keyDown = (event: KeyboardEvent) => {
    if (event.metaKey || event.ctrlKey || event.nativeEvent.isComposing) return;
    if (event.key === "Escape" && expanded) {
      event.preventDefault(); event.stopPropagation(); setOpen(false);
    } else if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      setActive(event.key === "Home" ? 0 : event.key === "End" ? options.length - 1
        : !expanded ? selected : (active + (event.key === "ArrowDown" ? 1 : -1) + options.length) % options.length);
      setOpen(true);
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (expanded) choose(active);
      else { setActive(selected); setOpen(true); }
    } else if (event.key === "Tab") setOpen(false);
    else if (event.key.length === 1 && !event.altKey) {
      const match = options.findIndex(option => option.label.toLowerCase().startsWith(event.key.toLowerCase()));
      if (match >= 0) { event.preventDefault(); setActive(match); setOpen(true); }
    }
  };
  return <div ref={root} className={`chip-select ${className}`} onBlur={event => {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) { setOpen(false); onBlur?.(); }
  }}>
    <button ref={trigger} type="button" role="combobox" aria-label={label} form={form}
      aria-expanded={expanded} aria-haspopup="listbox" aria-controls={expanded ? id : undefined}
      aria-activedescendant={expanded ? `${id}-${active}` : undefined}
      disabled={disabled} className="chip-select-trigger" onKeyDown={keyDown}
      onClick={() => { setActive(selected); setOpen(!expanded); }}>
      {options[selected].icon && <span className="chip-select-icon" aria-hidden="true">{options[selected].icon}</span>}
      <span>{options[selected].label}</span><span className="chip-select-chevron" aria-hidden="true" />
    </button>
    {expanded && <div id={id} className={`chip-select-menu align-${align}`} role="listbox" aria-label={label}>
      {options.map((option, index) => <button key={option.value} id={`${id}-${index}`} type="button" role="option"
        aria-selected={option.value === value} aria-label={option.label} tabIndex={-1}
        className={`chip-select-option${active === index ? " active" : ""}`}
        onPointerDown={event => event.preventDefault()} onMouseDown={event => event.preventDefault()}
        onPointerMove={() => setActive(index)} onClick={() => choose(index)}>
        {option.icon && <span className="chip-select-icon" aria-hidden="true">{option.icon}</span>}
        <span>{option.label}</span>{option.value === value && <span className="chip-select-check" aria-hidden="true">✓</span>}
      </button>)}
    </div>}
  </div>;
}
