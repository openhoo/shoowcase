import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

// The caller supplies the currently selectable rows in this table, never the
// entire workspace. Dropping or filtering a row also drops it from the action.
export function useSelection(ids: string[]) {
  const key = JSON.stringify(ids);
  const [state, set] = useState({ key, ids: new Set<string>() });
  const selected = state.key === key ? state.ids : new Set<string>();
  const allowed = new Set(ids);
  const count = selected.size;
  const all = count > 0 && count === allowed.size;
  return {
    selected,
    all,
    mixed: count > 0 && !all,
    count,
    toggle(id: string) {
      if (!allowed.has(id)) return;
      set((prev) => {
        const next = new Set(prev.key === key ? prev.ids : []);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        return { key, ids: next };
      });
    },
    toggleAll() {
      set({ key, ids: all ? new Set() : allowed });
    },
  };
}
type Selection = ReturnType<typeof useSelection>;
const check =
  "size-4 shrink-0 cursor-pointer accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring";
export function SelectAll({
  selection,
  label = "Select all on this page",
}: {
  selection: Selection;
  label?: string;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => {
    if (ref.current) ref.current.indeterminate = selection.mixed;
  }, [selection.mixed]);
  return (
    <label className="inline-flex min-h-9 cursor-pointer items-center gap-2 text-sm font-medium">
      <input
        ref={ref}
        data-select-all
        type="checkbox"
        checked={selection.all}
        onChange={selection.toggleAll}
        className={check}
      />
      {label}
    </label>
  );
}
export function SelectRow({
  selection,
  id,
  label,
  form,
  name = "selected",
  kind,
  className,
}: {
  selection: Selection;
  id: string;
  label: string;
  form?: string;
  name?: string;
  kind?: string;
  className?: string;
}) {
  return (
    <label className="inline-flex size-11 cursor-pointer items-center justify-center">
      <input
        type="checkbox"
        form={form}
        name={name}
        value={id}
        checked={selection.selected.has(id)}
        onChange={() => selection.toggle(id)}
        data-select-row
        data-selection-kind={kind}
        aria-label={label}
        className={cn(check, className)}
      />
    </label>
  );
}
export function SelectionCount({
  selection,
  detail,
}: {
  selection: Selection;
  detail?: string;
}) {
  return (
    <span
      className="text-sm text-muted-foreground tabular-nums"
      role="status"
      data-selection-count
    >
      {selection.count} selected{detail}
    </span>
  );
}
export function exportCSV(
  filename: string,
  header: string[],
  rows: string[][],
) {
  if (!rows.length) return;
  const field = (v: string) =>
    '"' +
    (/^[\s]*[=+@-]|^[\t\r\n]/u.test(v) ? "'" + v : v).replaceAll('"', '""') +
    '"';
  const csv = [header, ...rows]
    .map((row) => row.map(field).join(","))
    .join("\r\n");
  const url = URL.createObjectURL(
    new Blob(["\uFEFF", csv, "\r\n"], { type: "text/csv;charset=utf-8" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = filename.endsWith(".csv") ? filename : filename + ".csv";
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
