type DatedTask = { id: string; done_at?: string };
const instant = (raw?: string) => raw ? Date.parse(raw) : NaN;
const idOrder = (a: DatedTask, b: DatedTask) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0;

export function compareDoneTasks(a: DatedTask, b: DatedTask): number {
  const x = instant(a.done_at), y = instant(b.done_at);
  if (!Number.isFinite(x)) return Number.isFinite(y) ? 1 : idOrder(a, b);
  if (!Number.isFinite(y)) return -1;
  // Date truncates sub-millisecond precision; storage timestamps retain nanoseconds.
  const fraction = (raw: string) => (raw.match(/\.(\d+)/)?.[1] || "").padEnd(9, "0").slice(3, 9);
  return y - x || Number(fraction(b.done_at!)) - Number(fraction(a.done_at!)) || idOrder(a, b);
}

const calendarKey = (date: Date) => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
export function doneDay(raw?: string): string {
  const value = instant(raw);
  return Number.isFinite(value) ? calendarKey(new Date(value)) : "unknown";
}

export function doneDayLabel(key: string, now = new Date()): string {
  if (key === "unknown") return "Unknown date";
  if (key === calendarKey(now)) return "Today";
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  if (key === calendarKey(yesterday)) return "Yesterday";
  const [year, month, day] = key.split("-").map(Number);
  return new Date(year, month - 1, day).toLocaleDateString([], {
    day: "numeric", month: "short", ...(year !== now.getFullYear() ? { year: "numeric" } : {}),
  });
}
