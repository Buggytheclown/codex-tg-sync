import { expect, it } from "vitest";
import { compareDoneTasks, doneDay, doneDayLabel } from "./doneChronology";
import { placeTask } from "./taskOrder";

it("orders Done by last entry, unknowns last, stable ties including sub-millisecond instants", () => {
 const a = { id:"a", done_at:"2026-11-09T10:00:00.000001Z" };
 const b = { id:"b", done_at:"2026-11-09T10:00:00.000002Z" };
 const c = { id:"c", done_at:"2026-11-08T18:00:00Z" };
 expect([{ id:"unknown" },c,a,b].sort(compareDoneTasks).map(t=>t.id)).toEqual(["b","a","c","unknown"]);
 expect(compareDoneTasks(a,{...a,id:"z"})).toBeLessThan(0);
 expect(compareDoneTasks({id:"a",done_at:"invalid"},{id:"b"})).toBeLessThan(0);
});

it("groups by browser calendar days, labels yesterday across DST and includes older years", () => {
 const today = new Date(2026,10,9,0,5);
 const yesterday = new Date(2026,10,8,23,59);
 expect(doneDay(yesterday.toISOString())).not.toBe(doneDay(today.toISOString()));
 expect(doneDayLabel(doneDay(today.toISOString()),today)).toBe("Today");
 expect(doneDayLabel(doneDay(yesterday.toISOString()),today)).toBe("Yesterday");
 for (const [month, day] of [[2, 9], [2, 30], [9, 26], [10, 2]]) {
   const afterChange = new Date(2026, month, day, 0, 5);
   const previousDay = new Date(2026, month, day - 1, 0, 5);
   expect(doneDayLabel(doneDay(previousDay.toISOString()), afterChange)).toBe("Yesterday");
 }
 expect(doneDayLabel(doneDay(new Date(2025,11,31).toISOString()),today)).toContain("2025");
 expect(doneDayLabel(doneDay("invalid"),today)).toBe("Unknown date");
});

it("optimistically dates cross-column completion and preserves same-Done dates", () => {
 const task={id:"a",column:"todo" as const,position:1,done_at:""};
 const old={id:"b",column:"done" as const,position:2,done_at:"2026-11-08T00:00:00Z"};
 const moved=placeTask([task,old],task,"done",old.id,"2026-11-09T12:00:00Z");
 expect(moved.find(t=>t.id==="a")?.done_at).toBe("2026-11-09T12:00:00Z");
 expect(placeTask(moved,moved[0],"done","b")).toEqual(moved);
 expect(placeTask(moved,moved[0],"todo","")[0].done_at).toBe("");
});
