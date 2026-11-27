import { ActionButton } from "@/components/icon-action";
import { PanelSection } from "@/components/panel-section";
import {
  SelectAll,
  SelectionCount,
  type useSelection,
} from "@/components/selection";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { navigationTarget, postTarget } from "@/lib/navigation";
import { type ComponentProps, type ReactNode } from "react";
import { longDate, role } from "./members-model";
import { linkStyle, sectionHeading, toolbar } from "./sites-ui";
export {
  Cell,
  NameCell,
  PickCell,
  RecordRow,
  RecordTable,
} from "@/components/record-table";

export const stack = "grid min-w-0 gap-4";
export const secondaryText = "text-sm text-muted-foreground";
export const summaryClass =
  "w-fit max-w-full cursor-pointer rounded-sm font-medium focus-visible:outline-2 focus-visible:outline-ring";
export const reviewList = "list-disc space-y-1 break-words pl-6";
export const groupChoices = ["reader", "editor", "publisher"].map(
  (value) => [value, role(value)] as const,
);
export type Selection = ReturnType<typeof useSelection>;

export function Hidden({
  fields,
}: {
  fields: Record<string, string | number | boolean | undefined>;
}) {
  return (
    <>
      {Object.entries(fields).map(
        ([name, v]) =>
          v !== undefined && (
            <input key={name} type="hidden" name={name} value={String(v)} />
          ),
      )}
    </>
  );
}
export function Post({
  path = "/admin",
  fields,
  children,
  ...props
}: Omit<ComponentProps<"form">, "action" | "method"> & {
  path?: string;
  fields: Record<string, string | number | boolean | undefined>;
}) {
  return (
    <form method="post" {...props} action={postTarget(path)}>
      <Hidden fields={fields} />
      {children}
    </form>
  );
}
export function Field({
  id,
  label,
  children,
  hint,
  hintId,
}: {
  id: string;
  label: ReactNode;
  children: ReactNode;
  hint?: ReactNode;
  hintId?: string;
}) {
  return (
    <div className="grid min-w-0 gap-2 [&_[data-slot=native-select-wrapper]]:w-full">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && (
        <small id={hintId ?? id + "-help"} className={secondaryText}>
          {hint}
        </small>
      )}
    </div>
  );
}
export function Choices({
  choices,
  ...props
}: ComponentProps<typeof NativeSelect> & {
  choices: readonly (readonly [string, string])[];
}) {
  return (
    <NativeSelect {...props}>
      {choices.map(([value, title]) => (
        <NativeSelectOption key={value} value={value}>
          {title}
        </NativeSelectOption>
      ))}
    </NativeSelect>
  );
}
export function Panel({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <PanelSection aria-labelledby={id}>
      <h2 className={sectionHeading} id={id} tabIndex={-1}>
        {title}
      </h2>
      {children}
    </PanelSection>
  );
}
export function Disclosure({
  title,
  children,
  open,
}: {
  title: string;
  children: ReactNode;
  open?: boolean;
}) {
  return (
    <details
      className="min-w-0 space-y-3 rounded-lg border bg-card p-4"
      open={open}
    >
      <summary className={summaryClass}>{title}</summary>
      {children}
    </details>
  );
}
export function DirectoryBox({
  id,
  title,
  count,
  control,
  children,
}: {
  id: string;
  title: string;
  count: string;
  control?: ReactNode;
  children: ReactNode;
}) {
  return (
    <PanelSection className="directory" aria-labelledby={id} data-selection>
      <div className={toolbar}>
        <h2 className={sectionHeading} id={id}>
          {title}
        </h2>
        <span className={secondaryText}>{count}</span>
        {control}
      </div>
      {children}
    </PanelSection>
  );
}
export function BulkForm({
  id,
  fields,
  path,
  selection,
  detail,
  children,
}: {
  id: string;
  path?: string;
  fields: Record<string, string>;
  selection: Selection;
  detail?: string;
  children: ReactNode;
}) {
  return (
    <Post
      id={id}
      fields={fields}
      path={path}
      className={
        toolbar + " selection-toolbar rounded-lg bg-muted/50 p-3 text-sm"
      }
    >
      <SelectAll selection={selection} label="Select all on this page" />
      <SelectionCount selection={selection} detail={detail} />
      {children}
    </Post>
  );
}
export function BulkButton(props: ComponentProps<typeof ActionButton>) {
  return (
    <ActionButton
      data-selection-submit
      variant="outline"
      type="submit"
      {...props}
    />
  );
}
export function DateDisplay({
  value,
  full = false,
}: {
  value: string;
  full?: boolean;
}) {
  const date = longDate(value);
  return (
    <time dateTime={value} title={date}>
      {full ? date : date.split(" at ")[0]}
    </time>
  );
}
export function Pager({
  label,
  prev,
  next,
  noun,
  older = false,
}: {
  label: string;
  prev?: string;
  next?: string;
  noun: string;
  older?: boolean;
}) {
  if (!prev && !next) return null;
  return (
    <nav aria-label={label} className={toolbar + " justify-between"}>
      {prev && (
        <a className={linkStyle} href={navigationTarget(prev)}>
          ← Previous {noun}
        </a>
      )}
      {next && (
        <a className={linkStyle} href={navigationTarget(next)}>
          {older ? "Older" : "Next"} {noun} →
        </a>
      )}
    </nav>
  );
}
