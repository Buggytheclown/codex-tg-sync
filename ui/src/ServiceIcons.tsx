import arcanum from "./assets/arcanum.svg";
import tracker from "./assets/tracker.svg";
import codex from "./assets/codex.png";

export function ServiceIcon({ service, className = "" }: { service: "arcanum" | "tracker" | "codex"; className?: string }) {
  const src = { arcanum, tracker, codex }[service];
  return <img className={`service-icon ${className}`} src={src} alt="" aria-hidden="true" draggable={false} />;
}

export function EditIcon() {
  return <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
    <path d="m10.5 2.5 3 3-7.75 7.75-3.5.5.5-3.5zM9 4l3 3" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
  </svg>;
}
