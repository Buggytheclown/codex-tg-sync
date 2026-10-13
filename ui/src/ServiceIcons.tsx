import arcanum from "./assets/arcanum.svg";
import tracker from "./assets/tracker.svg";
import codex from "./assets/codex.png";
import type { SourceKind } from "./model";

export function SourceIcon({ kind }: { kind: SourceKind }) {
  return <svg className="origin-icon" data-source-kind={kind} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {kind === "review" ? <><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" /></>
      : <><path d="M20.5 11.5a8.5 8.5 0 0 1-9 8.5 9 9 0 0 1-3.5-.8L3 21l1.4-5A8.5 8.5 0 0 1 3 11.5a8.8 8.8 0 0 1 17.5 0Z" /><path d="M8 10h8M8 14h5" /></>}
  </svg>;
}

export function ServiceIcon({ service, className = "" }: { service: "arcanum" | "tracker" | "codex"; className?: string }) {
  const src = { arcanum, tracker, codex }[service];
  return <img className={`service-icon ${className}`} src={src} alt="" aria-hidden="true" draggable={false} />;
}

export function EditIcon() {
  return <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
    <path d="m10.5 2.5 3 3-7.75 7.75-3.5.5.5-3.5zM9 4l3 3" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
  </svg>;
}
