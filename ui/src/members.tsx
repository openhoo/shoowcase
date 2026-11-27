import { navigationTarget } from "@/lib/navigation";
import {
  Activity,
  ArrowLeft,
  Check,
  Download,
  ExternalLink,
  Gauge,
  Globe,
  KeyRound,
  LayoutGrid,
  LockKeyhole,
  LogOut,
  Save,
  Settings2,
  UserRound,
  UserRoundPlus,
  UsersRound,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  exportCSV,
  SelectAll,
  SelectionCount,
  SelectRow,
  useSelection,
} from "@/components/selection";
import {
  ButtonLink,
  Empty,
  Head,
  linkStyle,
  Notice,
  panel,
  toolbar,
} from "./sites-ui";
import { Groups, MemberListing } from "./members-directory";
import {
  KeyList,
  KeyReceipt,
  KeyReview,
  Keys,
  QuickReady,
  Quickstart,
} from "./members-keys";
import { Quotas } from "./members-quotas";
import {
  type MemberData,
  type AuditEvent,
  longDate,
  plural,
  role,
} from "./members-model";
import {
  Cell,
  Choices,
  DateDisplay,
  DirectoryBox,
  Field,
  Hidden,
  NameCell,
  Pager,
  Panel,
  PickCell,
  Post,
  RecordRow,
  RecordTable,
  reviewList,
  secondaryText,
  stack,
} from "./members-ui";

function Account({ p }: { p: MemberData }) {
  if (!p.Enrolled)
    return (
      <>
        <Head tag={"Workspace · " + p.Workspace} title="Create your account">
          <p>Choose how your name appears to your workspace admin.</p>
        </Head>
        <Post
          path="/account"
          className={panel + " max-w-2xl"}
          fields={{
            csrf: p.CSRF,
            action: "enroll",
            next: p.Next === "quickstart" ? "quickstart" : undefined,
          }}
        >
          {p.Err && (
            <div id="account-error">
              <Notice text={p.Err} error />
            </div>
          )}
          <Field
            id="display-name"
            label={
              <>
                Display name <span className={secondaryText}>(optional)</span>
              </>
            }
            hintId="name-help"
            hint="Leave this blank to use your sign-in ID."
          >
            <Input
              id="display-name"
              name="display_name"
              defaultValue={p.NameValue}
              maxLength={80}
              autoComplete="nickname"
              aria-describedby={"name-help" + (p.Err ? " account-error" : "")}
              aria-invalid={p.Err ? true : undefined}
            />
          </Field>
          <Details
            entries={[
              ["Sign-in ID", <code>{p.Subject}</code>],
              ["Role", role(p.Role)],
            ]}
          />
          <Button>
            <UserRoundPlus aria-hidden="true" />
            Create account
          </Button>
        </Post>
      </>
    );
  return (
    <>
      <Head tag="Workspace" title="Your account" />
      <Panel id="account-details" title="Account details">
        <Details
          entries={[
            ["Display name", p.Name],
            ["Workspace", p.Workspace],
            ["Role", role(p.Role)],
            ["Sign-in ID", <code>{p.Subject}</code>],
          ]}
        />
        <div className={toolbar}>
          {p.LocalUser ? (
            <ButtonLink href="#change-password" primary>
              <KeyRound aria-hidden="true" />
              Change password
            </ButtonLink>
          ) : p.OIDCAccountURL ? (
            <ButtonLink
              external
              href={p.OIDCAccountURL}
              rel="noreferrer"
              primary
            >
              <ExternalLink aria-hidden="true" />
              Change password at identity provider
            </ButtonLink>
          ) : null}
          <ButtonLink href="/sites">
            <LayoutGrid aria-hidden="true" />
            Open sites
          </ButtonLink>
          <ButtonLink href="/keys">
            <KeyRound aria-hidden="true" />
            Manage agent keys
          </ButtonLink>
          <Post path="/auth/logout" fields={{ csrf: p.CSRF }}>
            <Button variant="outline">
              <LogOut aria-hidden="true" />
              Sign out
            </Button>
          </Post>
        </div>
      </Panel>
      {p.LocalUser ? (
        <Panel id="change-password" title="Change password">
          <p>Changing your password signs you out on all devices.</p>
          <Notice text={p.Err} error />
          <Post
            path="/account"
            className={stack + " max-w-lg"}
            fields={{ csrf: p.CSRF, action: "change-password" }}
          >
            <Field id="current-password" label="Current password">
              <Input
                id="current-password"
                type="password"
                name="current_password"
                autoComplete="current-password"
                required
              />
            </Field>
            <Field
              id="new-password"
              label="New password"
              hintId="password-help"
              hint="Use at least 12 characters."
            >
              <Input
                id="new-password"
                type="password"
                name="new_password"
                autoComplete="new-password"
                minLength={12}
                required
                aria-describedby="password-help"
              />
            </Field>
            <div>
              <Button>
                <LockKeyhole aria-hidden="true" />
                Change password
              </Button>
            </div>
          </Post>
        </Panel>
      ) : (
        !p.OIDCAccountURL && (
          <Panel id="work-password" title="Change password">
            <p>
              Open your work account’s security settings to change your
              password. If you don’t know where to find them, ask your workspace
              administrator.
            </p>
          </Panel>
        )
      )}
    </>
  );
}
function Details({ entries }: { entries: [string, React.ReactNode][] }) {
  return (
    <dl className="grid min-w-0 gap-3 sm:grid-cols-2">
      {entries.map(([name, value]) => (
        <div key={name} className="min-w-0">
          <dt className={secondaryText}>{name}</dt>
          <dd className="break-words font-medium">{value}</dd>
        </div>
      ))}
    </dl>
  );
}
function Settings({ p }: { p: MemberData }) {
  const s = p.Settings;
  return (
    <Panel id="settings" title="Workspace settings">
      <Post
        fields={{ csrf: p.CSRF, action: "workspace-save", version: s.Version }}
        className={stack}
      >
        <Field
          id="workspace-name"
          label="Workspace display name"
          hint={
            <>
              Permanent workspace ID: <code>{s.ID}</code>
            </>
          }
        >
          <Input
            id="workspace-name"
            name="display_name"
            maxLength={80}
            defaultValue={s.DisplayName}
            aria-describedby="workspace-name-help"
          />
        </Field>
        <Field id="access-mode" label="Access mode">
          <Choices
            id="access-mode"
            name="mode"
            defaultValue={s.Mode}
            aria-describedby="mode-help"
            choices={[
              ["identity", "Identity provider roles"],
              ["managed", "Managed members and groups"],
            ]}
          />
        </Field>
        <div id="mode-help" className={stack + " text-sm"}>
          <p>
            <strong>Identity provider roles:</strong> people with a valid
            workspace claim can use their sign-in permissions. Local role limits
            and suspensions still apply.
          </p>
          <p>
            <strong>Managed members and groups:</strong> only workspace members
            can access private sites. Members start as viewers; groups grant
            editing or publishing up to each person’s sign-in role and local
            role limit. Workspace administrators keep their administrative role.
          </p>
          <p>
            Prepare members and groups before switching. Saving changes access
            immediately, including existing agent keys.
          </p>
        </div>
        <div>
          <Button>
            <Save aria-hidden="true" />
            Save workspace settings
          </Button>
        </div>
      </Post>
    </Panel>
  );
}
function Audit({ p }: { p: MemberData }) {
  const rows = p.Events ?? [],
    s = useSelection(rows.map((e) => String(e.sequence)));
  const labels = (e: AuditEvent) =>
    [
      ["For", e.target],
      ["From", e.previous],
      ["To", e.value],
    ].filter((v): v is string[] => Boolean(v[1]));
  const cols = ["Event", "When (UTC)", "By", "Details"],
    name = (e: AuditEvent) => p.EventNames[e.action];
  function download() {
    exportCSV(
      "audit-events.csv",
      cols,
      rows
        .filter((e) => s.selected.has(String(e.sequence)))
        .map((e) => [
          name(e) + " #" + e.sequence,
          longDate(e.at),
          e.subject,
          labels(e)
            .map((v) => v.join(" "))
            .join(" ") || "—",
        ]),
    );
  }
  return (
    <DirectoryBox
      id="activity"
      title="Audit log"
      count={rows.length + " " + plural(rows.length, "event") + " on this page"}
    >
      <div>
        <p>
          Sign-ins and changes to workspace access, keys, drafts and releases.
          Most recent first.
        </p>
        <a className={linkStyle} href="/api/audit">
          Open JSON export
        </a>
      </div>
      {rows.length > 0 ? (
        <>
          <div
            className={
              toolbar + " selection-toolbar rounded-lg bg-muted/50 p-3 text-sm"
            }
          >
            <SelectAll selection={s} />
            <SelectionCount selection={s} />
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-selection-export="audit-events"
              disabled={!s.count}
              onClick={download}
            >
              <Download aria-hidden="true" />
              Download selected (CSV)
            </Button>
          </div>
          <RecordTable id="activity" columns={["Select", ...cols]}>
            {rows.map((e) => (
              <RecordRow key={e.sequence}>
                <PickCell>
                  <SelectRow
                    selection={s}
                    id={String(e.sequence)}
                    label={"Select event " + e.sequence}
                  />
                </PickCell>
                <NameCell>
                  <span>{name(e)}</span>
                  <span className={"site-address block " + secondaryText}>
                    #{e.sequence}
                  </span>
                </NameCell>
                <Cell label="When (UTC)">
                  <DateDisplay value={e.at} full />
                </Cell>
                <Cell label="By">
                  <code>{e.subject}</code>
                </Cell>
                <Cell label="Details">
                  {labels(e).length
                    ? labels(e).map(([l, v]) => (
                        <div key={l}>
                          {l} <code>{v}</code>
                        </div>
                      ))
                    : "—"}
                </Cell>
              </RecordRow>
            ))}
          </RecordTable>
        </>
      ) : (
        <Empty title="No events on this page">
          Sign-ins and workspace changes appear in this log.
        </Empty>
      )}
      <Pager label="Audit log pages" noun="events" older next={p.Next} />
    </DirectoryBox>
  );
}
const views = [
  ["members", "Members", UserRound],
  ["groups", "Groups", UsersRound],
  ["quotas", "Quotas", Gauge],
  ["settings", "Workspace settings", Settings2],
  ["keys", "Workspace keys", KeyRound],
  ["activity", "Activity", Activity],
] as const;
function Admin({ p }: { p: MemberData }) {
  return (
    <>
      <Head title="Workspace admin" tag={"Workspace / " + p.Workspace} />
      <Notice text={p.Notice} />
      <dl
        aria-label="Workspace totals"
        className="grid grid-cols-1 gap-3 min-[420px]:grid-cols-3"
      >
        {(
          [
            ["Members", p.Stats.Members, UsersRound],
            ["Sites", p.Stats.Sites, Globe],
            ["Active keys", p.Stats.ActiveKeys, KeyRound],
          ] as const
        ).map(([title, count, Icon]) => (
          <div
            key={title}
            className="min-w-0 rounded-lg border bg-card p-3 sm:p-4"
          >
            <dt className="flex items-center gap-2 text-sm text-muted-foreground">
              <Icon aria-hidden="true" className="size-4" />
              {title}
            </dt>
            <dd className="text-2xl font-semibold tabular-nums">{count}</dd>
          </div>
        ))}
      </dl>
      <nav
        className={toolbar + " border-b pb-4"}
        aria-label="Administration views"
      >
        {views.map(([view, title, Icon]) => (
          <Button
            asChild
            variant={p.View === view ? "secondary" : "ghost"}
            size="sm"
            key={view}
          >
            <a
              href={view === "members" ? "/admin" : "/admin?view=" + view}
              aria-current={p.View === view ? "page" : undefined}
            >
              <Icon aria-hidden="true" />
              {title}
            </a>
          </Button>
        ))}
      </nav>
      {p.View === "members" ? (
        <MemberListing p={p} />
      ) : p.View === "groups" ? (
        <Groups p={p} />
      ) : p.View === "settings" ? (
        <Settings p={p} />
      ) : p.View === "keys" ? (
        <KeyList p={p} />
      ) : p.View === "quotas" ? (
        <Quotas p={p} />
      ) : (
        <Audit p={p} />
      )}
    </>
  );
}
interface Review {
  Title: string;
  Effect: string;
  Button: string;
  Selection: { Action: string; Group: string; IDs: string[] };
  Names: string[];
  CSRF: string;
  Token: string;
  Back: string;
}
function SelectionReview({ p }: { p: Review }) {
  return (
    <>
      <a href={navigationTarget(p.Back)} className={linkStyle}>
        ← Back to directory
      </a>
      <section className={panel}>
        <Head tag="Review selection" title={p.Title} />
        <p>{p.Effect}</p>
        <p>
          <strong>Selected entries ({p.Names.length})</strong>
        </p>
        <ul className={reviewList}>
          {p.Names.map((v, i) => (
            <li key={p.Selection.IDs[i]}>{v}</li>
          ))}
        </ul>
        <Post
          className={toolbar}
          fields={{
            csrf: p.CSRF,
            action: "apply-selection",
            operation: p.Selection.Action,
            group: p.Selection.Group,
            review: p.Token,
          }}
        >
          {p.Selection.IDs.map((id) => (
            <Hidden key={id} fields={{ selected: id }} />
          ))}
          <Button
            name="confirm"
            value="yes"
            variant={
              p.Selection.Action === "restore-members"
                ? "default"
                : "destructive"
            }
          >
            <Check aria-hidden="true" />
            {p.Button}
          </Button>
          <ButtonLink href={p.Back}>
            <X aria-hidden="true" />
            Cancel
          </ButtonLink>
        </Post>
      </section>
    </>
  );
}
function ClearReview({
  p,
}: {
  p: {
    Title: string;
    Effect: string;
    Rows: { Value: string; Name: string }[];
    CSRF: string;
    Action: string;
    Cancel: string;
    Review: string;
  };
}) {
  return (
    <section className={panel}>
      <Head title={p.Title} tag="Review selection" />
      <p>{p.Effect}</p>
      <Post
        fields={{ csrf: p.CSRF, action: p.Action, review: p.Review }}
        className={stack}
      >
        <ul className={reviewList}>
          {p.Rows.map((r) => (
            <li key={r.Value}>
              {r.Name}
              <Hidden fields={{ selected: r.Value }} />
            </li>
          ))}
        </ul>
        <div className={toolbar}>
          <Button variant="destructive" name="confirm" value="yes">
            <Check aria-hidden="true" />
            {p.Title}
          </Button>
          <ButtonLink href={"/admin?view=quotas#" + p.Cancel}>
            <X aria-hidden="true" />
            Cancel
          </ButtonLink>
        </div>
      </Post>
    </section>
  );
}
// The Go handlers supply a separate presentation object for each page.
export function Members({ page }: { page: any }) {
  const p = page.data;
  let content;
  switch (page.view) {
    case "account":
      content = <Account p={p} />;
      break;
    case "keys":
      content = <Keys p={p} />;
      break;
    case "key-receipt":
      content = <KeyReceipt p={p} />;
      break;
    case "quick-start":
      content = <Quickstart p={p} />;
      break;
    case "quick-ready":
      content = <QuickReady p={p} />;
      break;
    case "admin":
      content = <Admin p={p} />;
      break;
    case "key-revocation-review":
      content = <KeyReview p={p} />;
      break;
    case "admin-selection-review":
      content = <SelectionReview p={p} />;
      break;
    case "confirm-clear-quotas":
      content = <ClearReview p={p} />;
      break;
    case "quota-error":
    case "admin-selection-error":
      content = (
        <section className={panel}>
          <Head tag="Workspace admin" title={p.Title} />
          <p>{p.Detail}</p>
          <ButtonLink href={p.Back ?? "/admin?view=quotas"}>
            <ArrowLeft aria-hidden="true" />
            {p.Back ? "Back to directory" : "Back to quotas"}
          </ButtonLink>
        </section>
      );
      break;
    default:
      throw new Error("Unknown member view: " + page.view);
  }
  return <div className="space-y-6">{content}</div>;
}
