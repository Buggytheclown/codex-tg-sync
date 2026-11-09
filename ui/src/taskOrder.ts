import { compareDoneTasks } from "./doneChronology";
import type { Column, TaskCard } from "./model";

// Match SQLite's order, including legacy cards sharing position zero.
export const compareTasks = (a: Pick<TaskCard, "id" | "position">, b: Pick<TaskCard, "id" | "position">) =>
  a.position - b.position || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

export const compareBoardTasks = (a: TaskCard, b: TaskCard) =>
  a.column === "done" && b.column === "done" ? compareDoneTasks(a, b) : compareTasks(a, b);

export function placeTask<T extends Pick<TaskCard, "id" | "position" | "column"> & { done_at?: string }>(tasks: T[], task: T, column: Column, beforeID: string, now = new Date().toISOString()): T[] {
  if (column === "done") return task.column === "done" ? tasks : tasks.map(t => t.id === task.id ? { ...t, column, done_at: now } : t);
  const destination = tasks.filter((t) => t.column === column && t.id !== task.id).sort(compareTasks);
  const anchor = destination.findIndex((t) => t.id === beforeID);
  destination.splice(anchor < 0 ? destination.length : anchor, 0, { ...task, column });
  const positions = new Map(destination.map((t, i) => [t.id, (i + 1) * 1024]));
  return tasks.map((t) => positions.has(t.id)
    ? { ...(t.id === task.id ? { ...t, column, done_at: "" } : t), position: positions.get(t.id)! }
    : t);
}
