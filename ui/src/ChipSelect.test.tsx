// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ChipSelect } from "./ChipSelect";

afterEach(cleanup);
const options = [
  { value: "inbox", label: "Inbox", icon: "↓" },
  { value: "todo", label: "TODO", icon: "●" },
  { value: "polling", label: "Polling", icon: "◷" },
  { value: "done", label: "Done", icon: "✓" },
];
it("opens on the current value, supports keyboard choice and cancels Escape locally", () => {
  const change = vi.fn(), escape = vi.fn();
  render(<div onKeyDown={escape}><ChipSelect label="Move task" value="todo" options={options} onChange={change} /></div>);
  const trigger = screen.getByRole("combobox", { name: "Move task" });
  fireEvent.click(trigger);
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByRole("option", { name: "TODO" }).getAttribute("aria-selected")).toBe("true");
  fireEvent.keyDown(trigger, { key: "End" });
  escape.mockClear();
  fireEvent.keyDown(trigger, { key: "Escape" });
  expect(change).not.toHaveBeenCalled();
  expect(escape).not.toHaveBeenCalled();
  expect(screen.queryByRole("listbox")).toBeNull();
  fireEvent.keyDown(trigger, { key: "ArrowDown" });
  fireEvent.keyDown(trigger, { key: "End" });
  fireEvent.keyDown(trigger, { key: "Enter" });
  expect(change).toHaveBeenCalledWith("done");
  expect(document.activeElement).toBe(trigger);
});
it("keeps form ownership, saves only on leaving the control, and closes on outside press", () => {
  const change = vi.fn(), blur = vi.fn();
  render(<><form id="overview" /><ChipSelect label="Priority" form="overview" value="todo" options={options} onChange={change} onBlur={blur} /></>);
  const trigger = screen.getByRole("combobox", { name: "Priority" }) as HTMLButtonElement;
  expect(trigger.form?.id).toBe("overview");
  trigger.focus();
  fireEvent.click(trigger);
  const option = screen.getByRole("option", { name: "Done" });
  fireEvent.pointerDown(option);
  fireEvent.click(option);
  expect(change).toHaveBeenCalledWith("done");
  expect(blur).not.toHaveBeenCalled();
  fireEvent.blur(trigger);
  expect(blur).toHaveBeenCalledOnce();
  fireEvent.click(trigger);
  fireEvent.pointerDown(document.body);
  expect(screen.queryByRole("listbox")).toBeNull();
});
it("does not change a selected value or open a disabled control", () => {
  const change = vi.fn();
  const view = render(<ChipSelect label="Move task" value="todo" options={options} onChange={change} />);
  fireEvent.click(screen.getByRole("combobox"));
  fireEvent.click(screen.getByRole("option", { name: "TODO" }));
  expect(change).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("combobox"));
  view.rerender(<ChipSelect label="Move task" value="todo" options={options} onChange={change} disabled />);
  fireEvent.click(screen.getByRole("combobox"));
  expect(screen.queryByRole("listbox")).toBeNull();
  view.rerender(<ChipSelect label="Move task" value="todo" options={options} onChange={change} />);
  expect(screen.queryByRole("listbox")).toBeNull();
});
