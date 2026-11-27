import { ActionLink, NavigationLink } from "@/components/icon-action";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { navigationTarget } from "@/lib/navigation";
import { ArrowLeft, File, FolderOpen } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import type { SiteData } from "./sites-model";

export const toolbar = "flex min-w-0 flex-wrap items-center gap-2.5";
export const panel =
  "space-y-5 rounded-xl border bg-card p-4 text-card-foreground sm:p-6";
export const sectionHeading = "text-xl font-semibold tracking-tight";
export const linkStyle =
  "text-primary underline decoration-primary/40 underline-offset-4 hover:decoration-current";
export function Hidden({ name, value }: { name: string; value: string }) {
  return <input type="hidden" name={name} value={value} />;
}
export function Token({ d }: { d: SiteData }) {
  return <Hidden name="csrf" value={d.C.Identity.CSRF} />;
}
export function Write({ d }: { d: SiteData }) {
  return (
    <>
      <Token d={d} />
      <Hidden name="request_id" value={d.Request} />
      <Hidden name="revision" value={d.Rev} />
    </>
  );
}

export function ButtonLink({
  href,
  primary = false,
  action = false,
  external = false,
  ...props
}: ComponentProps<typeof ActionLink> & {
  href: string;
  primary?: boolean;
  action?: boolean;
  external?: boolean;
}) {
  const Link = action ? ActionLink : NavigationLink;
  return (
    <Link
      {...props}
      variant={primary ? "default" : "outline"}
      href={navigationTarget(href, external)}
    />
  );
}
export function Back({ d }: { d?: SiteData }) {
  return (
    <a
      className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      href={navigationTarget(d?.Base || "/sites")}
    >
      <ArrowLeft aria-hidden="true" className="size-4" />
      {d?.Base ? "Back to site" : "All sites"}
    </a>
  );
}
export function Head({
  tag,
  title,
  children,
  action,
}: {
  tag: string;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <header className="flex min-w-0 flex-wrap items-end justify-between gap-4">
      <div className="min-w-0 space-y-2">
        <p className="text-xs font-semibold uppercase tracking-[.12em] text-muted-foreground">
          {tag}
        </p>
        <h1 className="break-words text-3xl font-semibold tracking-tight sm:text-4xl">
          {title}
        </h1>
        {children}
      </div>
      {action}
    </header>
  );
}
export function Field({
  id,
  label,
  hint,
  children,
}: {
  id: string;
  label: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="grid min-w-0 gap-2 [&_[data-slot=native-select-wrapper]]:w-full">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && (
        <small className="text-muted-foreground" id={id + "-hint"}>
          {hint}
        </small>
      )}
    </div>
  );
}
export function Notice({
  text,
  error = false,
}: {
  text: string;
  error?: boolean;
}) {
  if (!text) return null;
  return (
    <div
      role={error ? "alert" : "status"}
      className={
        error
          ? "notice rounded-md border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive"
          : "notice rounded-md border bg-muted/50 px-4 py-3 text-sm"
      }
    >
      {error && <strong>Changes not saved. </strong>}
      {text}
    </div>
  );
}
export function Empty({
  title,
  children,
}: {
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="space-y-2 rounded-lg border border-dashed p-5 text-center sm:p-8">
      <FolderOpen
        className="mx-auto size-5 text-muted-foreground"
        aria-hidden="true"
      />
      <h3 className="font-semibold">{title}</h3>
      {children && (
        <div className="space-y-2 text-sm text-muted-foreground">
          {children}
        </div>
      )}
    </div>
  );
}
export function StateBadge({ value, label }: { value: string; label: string }) {
  return (
    <Badge variant="secondary" className="gap-1.5 whitespace-normal">
      <span
        aria-hidden="true"
        className={
          "size-2 shrink-0 rounded-full " +
          (value === "published"
            ? "bg-emerald-600"
            : value === "changes"
              ? "bg-amber-600"
              : value === "offline"
                ? "bg-slate-500"
                : "bg-sky-600")
        }
      />
      {label}
    </Badge>
  );
}
export function FileIcon() {
  return (
    <File
      className="size-4 shrink-0 text-muted-foreground"
      aria-hidden="true"
    />
  );
}
export function NativeChoice({
  id,
  options,
  value,
  name,
  required,
  hint,
}: {
  id: string;
  name: string;
  value: string;
  options: readonly (readonly [string, string])[];
  required?: boolean;
  hint?: boolean;
}) {
  // Keep a native select so ordinary form submission and mobile pickers use
  // exactly the same values as the Go handler.
  return (
    <NativeSelect
      id={id}
      name={name}
      required={required}
      defaultValue={value}
      aria-describedby={hint ? id + "-hint" : undefined}
      className="w-full"
    >
      {options.map(([v, label]) => (
        <NativeSelectOption value={v} key={v}>
          {label}
        </NativeSelectOption>
      ))}
    </NativeSelect>
  );
}
