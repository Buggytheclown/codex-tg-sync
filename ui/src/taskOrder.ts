import type { Column, Task } from "./model";

// Match SQLite's order, including legacy cards sharing position zero.
export const compareTasks = (a: Task, b: Task) =>
  a.position - b.position || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

export function placeTask(tasks: Task[], task: Task, column: Column, beforeID: string): Task[] {
  const destination = tasks.filter((t) => t.column === column && t.id !== task.id).sort(compareTasks);
  const anchor = destination.findIndex((t) => t.id === beforeID);
  destination.splice(anchor < 0 ? destination.length : anchor, 0, { ...task, column });
  const positions = new Map(destination.map((t, i) => [t.id, (i + 1) * 1024]));
  return tasks.map((t) => positions.has(t.id)
    ? { ...(t.id === task.id ? { ...t, column } : t), position: positions.get(t.id)! }
    : t);
}
