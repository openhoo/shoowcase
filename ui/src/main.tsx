import {
  ActionButton,
  NavigationLink,
  ActionTooltipProvider,
} from "@/components/icon-action";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  ArrowLeft,
  CheckCircle2,
  CircleUserRound,
  FileCode2,
  KeyRound,
  Layers3,
  LayoutGrid,
  LogOut,
  Settings2,
  ShieldAlert,
  Terminal,
} from "lucide-react";
import { useEffect } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { protectReceipt } from "./lib/receipt";
import { Members } from "./members";
import { Public } from "./public";
import { Sites } from "./sites";

interface Context {
  Name: string;
  Enrolled: boolean;
  WorkspaceLabel: string;
  Actor: { Role: string; Subject: string; Workspace: string };
  Identity: { Subject: string; CSRF: string };
}
interface ConsolePage {
  title: string;
  active: string;
  view: string;
  context: Context;
  data: any;
}

function Message({ page }: { page: ConsolePage }) {
  const { data, context: c, view } = page;
  const changed = view === "password-changed",
    provisioning = view === "provisioning-required";
  const Icon = changed ? CheckCircle2 : ShieldAlert;
  return (
    <Card className="mx-auto max-w-xl">
      <CardHeader>
        <Icon aria-hidden className="mb-2 size-6 text-primary" />
        <CardTitle>
          <h1 className="text-2xl">{page.title}</h1>
        </CardTitle>
        <CardDescription>
          {changed ? (
            "Your previous sign-ins have ended."
          ) : provisioning ? (
            <>
              Contact a workspace administrator and ask them to add you to{" "}
              <strong>{c.WorkspaceLabel}</strong>. Give them your exact sign-in
              ID: <code>{c.Identity.Subject}</code>. Once they add you, reload
              this page.
            </>
          ) : (
            data.Detail
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-3">
        {!provisioning && (
          <NavigationLink
            variant="outline"
            href={changed ? "/auth/login?next=/account" : "/"}
            label={
              changed ? "Sign in with your new password" : "Back to workspace"
            }
            icon={ArrowLeft}
          />
        )}
        {c.Identity.CSRF && (
          <form action="/auth/logout" method="post">
            <input type="hidden" name="csrf" value={c.Identity.CSRF} />
            <ActionButton
              variant="outline"
              type="submit"
              label="Sign out"
              icon={LogOut}
            />
          </form>
        )}
      </CardContent>
    </Card>
  );
}
function Console({ page }: { page: ConsolePage }) {
  // Links from another directory may name a section or a specific record.
  // Those targets enter the DOM only after React commits the new document.
  useEffect(() => {
    if (!location.hash) return;
    let id: string;
    try {
      id = decodeURIComponent(location.hash.slice(1));
    } catch {
      return;
    }
    const target = document.getElementById(id);
    if (!target) return;
    target.scrollIntoView({ block: "start" });
    if (!target.hasAttribute("tabindex") && target.tabIndex < 0)
      target.tabIndex = -1;
    target.focus({ preventScroll: true });
  }, [page]);
  const { context: c, active, view } = page;
  const links = [
    { name: "Sites", id: "sites", icon: LayoutGrid },
    { name: "Quickstart", id: "quickstart", icon: Terminal },
    { name: "Agent keys", id: "keys", icon: KeyRound },
    ...(c.Actor.Role === "admin"
      ? [{ name: "Admin", id: "admin", icon: Settings2 }]
      : []),
    { name: "Account", id: "account", icon: CircleUserRound },
  ];
  const special = [
    "error-page",
    "password-changed",
    "provisioning-required",
  ].includes(view);
  return (
    <div className="flex min-h-dvh min-w-0 flex-col">
      <header className="border-b border-border bg-card">
        <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center justify-between gap-x-8 gap-y-3 px-4 py-3 sm:px-7 lg:flex-nowrap lg:px-10">
          <a
            className="inline-flex items-center gap-2.5 font-semibold tracking-tight"
            href="/"
          >
            <span className="grid size-8 place-items-center rounded-lg bg-primary text-primary-foreground">
              <Layers3 className="size-5" aria-hidden />
            </span>
            Shoowcase
          </a>
          {c.Enrolled && (
            <nav
              aria-label="Workspace"
              className="order-3 flex w-full flex-wrap items-center gap-0.5 sm:gap-1 lg:order-none lg:flex-1"
            >
              {links.map(({ name, id, icon: Icon }) => (
                <NavigationLink
                  key={id}
                  variant={active === id ? "secondary" : "ghost"}
                  className="has-[>svg]:px-2 aria-[current=page]:ring-1 aria-[current=page]:ring-primary sm:has-[>svg]:px-3"
                  aria-current={active === id ? "page" : undefined}
                  href={"/" + id}
                  label={name}
                  icon={Icon}
                />
              ))}
            </nav>
          )}
          {c.WorkspaceLabel && (
            <span className="flex min-w-0 max-w-48 items-center gap-2 truncate text-xs font-medium text-muted-foreground">
              <FileCode2 aria-hidden className="size-4 shrink-0" />
              <span className="truncate">{c.WorkspaceLabel}</span>
            </span>
          )}
        </div>
      </header>
      <main
        className="mx-auto w-full min-w-0 max-w-7xl flex-1 px-4 py-6 focus:outline-none sm:px-7 sm:py-8 lg:px-10"
        id="content"
        tabIndex={-1}
      >
        {special ? (
          <Message page={page} />
        ) : active === "sites" ? (
          <Sites page={page} />
        ) : (
          <Members page={page} />
        )}
      </main>
      <footer className="border-t">
        <div className="mx-auto flex max-w-7xl flex-wrap justify-between gap-2 px-4 py-5 text-xs text-muted-foreground sm:px-7 lg:px-10">
          <span>Shoowcase</span>
          <span>{c.WorkspaceLabel}</span>
        </div>
      </footer>
    </div>
  );
}
function hasFormEdits(native: HTMLElement) {
  for (const field of native.querySelectorAll("input, textarea, select")) {
    if (field instanceof HTMLSelectElement) {
      // Let the browser resolve the original selection, including implicit
      // defaults, disabled options and multiple selections. Reset only a copy.
      const copy = field.cloneNode(true) as HTMLSelectElement;
      copy.removeAttribute("form");
      const form = document.createElement("form");
      form.append(copy);
      form.reset();
      if (
        Array.from(field.options).some(
          (o, i) => o.selected !== copy.options[i].selected,
        )
      )
        return true;
    } else if (field instanceof HTMLTextAreaElement) {
      if (field.value !== field.defaultValue) return true;
    } else if (field instanceof HTMLInputElement) {
      if (field.type === "checkbox" || field.type === "radio") {
        if (field.checked !== field.defaultChecked) return true;
      } else if (field.type === "file") {
        if (field.files?.length) return true;
      } else if (field.value !== field.defaultValue) {
        return true;
      }
    }
  }
  return false;
}

function mount() {
  const payload = document.getElementById("shoowcase-page");
  const host = document.getElementById("app");
  if (
    !(payload instanceof HTMLScriptElement) ||
    payload.type !== "application/json" ||
    !host
  )
    return;
  const native = document.getElementById("native-page");
  payload.remove();
  // Keep the native form, including the caret, selection and any chosen files,
  // if someone started using it while the bundle was downloading.
  if (
    native &&
    (native.contains(document.activeElement) ||
      hasFormEdits(native) ||
      native.querySelector(
        "input:user-valid, input:user-invalid, textarea:user-valid, textarea:user-invalid",
      ))
  ) {
    const receipt = native.querySelector("[data-secret-receipt]");
    if (receipt)
      protectReceipt(receipt.id === "setup-command" ? "/quickstart" : "/keys");
    return;
  }
  const { kind, page } = JSON.parse(payload.text);
  // Replace the native UI in one turn. An input event cannot land between
  // this focus check and the first React commit.
  const root = createRoot(host);
  flushSync(() =>
    root.render(
      <ActionTooltipProvider>
        {kind === "public" ? <Public page={page} /> : <Console page={page} />}
      </ActionTooltipProvider>,
    ),
  );
}
mount();
