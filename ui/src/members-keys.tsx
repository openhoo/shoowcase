import { protectReceipt } from "./lib/receipt";
import { ActionButton, ActionSummary } from "@/components/icon-action";
import {
  ArrowLeft,
  Ban,
  ClipboardCopy,
  KeyRound,
  Plus,
  Terminal,
  X,
} from "lucide-react";
import { useLayoutEffect, useState, type ReactNode } from "react";

import { SelectRow, useSelection } from "@/components/selection";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  plural,
  role,
  type AgentKey,
  type MemberData,
  type QuickData,
} from "./members-model";
import {
  BulkButton,
  BulkForm,
  Cell,
  Choices,
  DateDisplay,
  DirectoryBox,
  Disclosure,
  Field,
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
import {
  ButtonLink,
  Empty,
  Head,
  linkStyle,
  Notice,
  StateBadge,
  toolbar,
} from "./sites-ui";

const lifetimes = ["7", "30", "90"].map((v) => [v, v + " days"] as const);
const scopes: Record<string, string> = {
  reader: "Viewer · Read sites",
  editor: "Editor · Create and edit",
  publisher: "Publisher · Publish and manage public access",
};
const quickScopes: Record<string, string> = {
  reader: "View sites",
  editor: "Create and edit drafts",
  publisher: "Edit, publish and manage public access",
};

function Copy({ id, children }: { id: string; children: string }) {
  const [message, set] = useState("");
  async function copy() {
    const field = document.getElementById(id) as
      HTMLInputElement | HTMLTextAreaElement | null;
    if (!field || !field.value) return;
    try {
      await navigator.clipboard.writeText(field.value);
      set("Copied.");
    } catch {
      field.focus();
      field.select();
      set("Selected. Use your device’s copy command.");
    }
  }
  return (
    <div className={toolbar}>
      <ActionButton
        variant="outline"
        type="button"
        data-copy-target={id}
        onClick={copy}
        label={children}
        icon={ClipboardCopy}
      />
      <span role="status" className={secondaryText}>
        {message}
      </span>
    </div>
  );
}
function Endpoint({ value }: { value: string }) {
  return (
    <Field id="mcp-endpoint" label="MCP server URL">
      <Input
        type="url"
        id="mcp-endpoint"
        readOnly
        value={value}
        spellCheck={false}
      />
      <Copy id="mcp-endpoint">Copy URL</Copy>
    </Field>
  );
}
// A history entry can retain form values and the document even when the server
// disallows caching. Clear both copies before freezing the page and return to a
// fresh GET if the browser restores the receipt.
function useReceipt(
  data: MemberData | QuickData,
  field: "Command" | "Secret",
  back: string,
) {
  useLayoutEffect(
    () =>
      protectReceipt(back, () => {
        if (field === "Command") (data as QuickData).Command = "";
        else data.Secret = "";
      }),
    [data, field, back],
  );
}
export function KeyReceipt({ p }: { p: MemberData }) {
  useReceipt(p, "Secret", "/keys");
  return (
    <>
      <Head tag={"Agent keys · " + p.Workspace} title="Connect your agent">
        <p>
          Your key for <strong>{p.NameValue}</strong> is ready. Copy it before
          leaving this page.
        </p>
      </Head>
      <Panel id="connection" title="Connection details">
        <Endpoint value={p.Endpoint} />
        <Field
          id="agent-secret"
          label="Secret key"
          hint="You can only see this key here. Keep it in your agent’s credential settings."
          hintId="secret-help"
        >
          <Input
            id="agent-secret"
            data-secret-receipt
            readOnly
            defaultValue={p.Secret}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            aria-describedby="secret-help"
          />
          <Copy id="agent-secret">Copy key</Copy>
        </Field>
        <ol className="list-decimal space-y-2 pl-6">
          <li>
            In your agent’s MCP settings, add a <strong>Streamable HTTP</strong>{" "}
            server using the URL above.
          </li>
          <li>
            Choose bearer token authentication and paste the secret key. If your
            agent uses HTTP headers, set <code>Authorization</code> to{" "}
            <code>Bearer </code> followed by the key.
          </li>
          <li>Connect the server and ask your agent to list your sites.</li>
        </ol>
        <ButtonLink href="/keys" label="Back to agent keys" icon={ArrowLeft} />
      </Panel>
    </>
  );
}
export function KeyList({ p }: { p: MemberData }) {
  const rows = p.Keys ?? [],
    active = rows.filter((k) => k.State === "Active").map((k) => k.ID),
    s = useSelection(active);
  const title = p.Admin ? "Workspace keys" : "Your keys",
    id = p.Admin ? "workspace-keys" : "your-keys",
    path = p.Admin ? "/admin" : "/keys";
  return (
    <DirectoryBox
      id={id}
      title={title}
      count={rows.length + " " + plural(rows.length, "key") + " on this page"}
    >
      {p.Admin && <p>Revoking a key disconnects every agent using it.</p>}
      {active.length > 0 && (
        <BulkForm
          path={path}
          id="select-keys"
          selection={s}
          fields={{ csrf: p.CSRF, action: "review-key-revocation" }}
        >
          <BulkButton
            disabled={!s.count}
            label="Revoke selected keys"
            icon={Ban}
          />
        </BulkForm>
      )}
      {rows.length > 0 ? (
        <RecordTable
          id={id}
          columns={[
            ...(active.length ? ["Select"] : []),
            p.Admin ? "Key / owner" : "Key",
            "Status",
            "Access",
            "Created",
            "Expires",
            "Action",
          ]}
        >
          {rows.map((k) => (
            <RecordRow key={k.ID}>
              {active.length > 0 && (
                <PickCell>
                  {k.State === "Active" ? (
                    <SelectRow
                      name="key_id"
                      id={k.ID}
                      selection={s}
                      form="select-keys"
                      label={
                        "Select " +
                        k.Name +
                        (p.Admin ? ", owner " + k.Subject : "")
                      }
                    />
                  ) : (
                    "—"
                  )}
                </PickCell>
              )}
              <NameCell>
                <span className="inline-flex min-w-0 items-center gap-2">
                  <KeyRound
                    className="size-4 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                  {k.Name}
                </span>
                {p.Admin && (
                  <span className={"site-address block " + secondaryText}>
                    {k.Subject}
                  </span>
                )}
              </NameCell>
              <Cell label="Status">
                <StateBadge
                  value={k.State === "Active" ? "published" : "offline"}
                  label={k.State}
                />
              </Cell>
              <Cell label="Access">{role(k.Scope)}</Cell>
              <Cell label="Created">
                <DateDisplay value={k.CreatedAt} />
              </Cell>
              <Cell label="Expires">
                <DateDisplay value={k.ExpiresAt} />
              </Cell>
              <Cell>
                {k.State === "Active" ? (
                  <details className="min-w-0 space-y-3">
                    <ActionSummary
                      className="text-destructive"
                      label={"Revoke " + k.Name}
                      icon={Ban}
                    />
                    <Post
                      path={path}
                      className={stack}
                      fields={{ csrf: p.CSRF, action: "revoke", key_id: k.ID }}
                    >
                      <label>
                        <input
                          type="checkbox"
                          required
                          name="confirm"
                          value="yes"
                          className="mr-2"
                        />
                        Disconnect agents using this key.
                      </label>
                      <div>
                        <ActionButton
                          variant="destructive"
                          label="Revoke key"
                          icon={Ban}
                        />
                      </div>
                    </Post>
                  </details>
                ) : (
                  "—"
                )}
              </Cell>
            </RecordRow>
          ))}
        </RecordTable>
      ) : (
        <Empty title="No keys on this page">
          {p.Admin ? undefined : "Create a key to connect an agent."}
        </Empty>
      )}
      <Pager label="Key pages" noun="keys" next={p.Next} />
    </DirectoryBox>
  );
}
export function Keys({ p }: { p: MemberData }) {
  return (
    <>
      <Head
        tag={"Workspace / " + p.Workspace}
        title="Agent keys"
        action={
          <ButtonLink
            action
            primary
            href="#new-key"
            label="Create a key"
            icon={Plus}
          />
        }
      >
        <p>Use a separate key for each agent connection.</p>
        <a className={linkStyle} href="/quickstart">
          Set up an agent with Quickstart →
        </a>
      </Head>
      <Notice text={p.Notice} />
      <KeyList p={p} />
      <Panel id="new-key" title="Create a key">
        <Post
          path="/keys"
          className={stack}
          fields={{
            csrf: p.CSRF,
            action: "create",
            creation_token: p.Creation,
          }}
        >
          {p.Err && (
            <div id="key-error">
              <Notice text={p.Err} error />
            </div>
          )}
          <Field id="key-name" label="Key name">
            <Input
              id="key-name"
              name="name"
              defaultValue={p.NameValue}
              type="text"
              required
              maxLength={80}
              placeholder="e.g. Design team agent"
              aria-describedby={p.Err ? "key-error" : undefined}
            />
          </Field>
          <div className="grid min-w-0 gap-4 md:grid-cols-2">
            <Field id="key-scope" label="Access">
              <Choices
                name="scope"
                id="key-scope"
                defaultValue={p.Scope}
                required
                choices={p.Scopes.map((v) => [v, scopes[v]])}
              />
            </Field>
            <Field id="key-days" label="Expires in">
              <Choices
                id="key-days"
                name="days"
                defaultValue={p.Days}
                choices={lifetimes}
              />
            </Field>
          </div>
          <div>
            <ActionButton label="Create key" icon={KeyRound} />
          </div>
        </Post>
      </Panel>
      <Disclosure title="Connect an existing key">
        <p>
          In your agent’s MCP settings, choose Streamable HTTP. Enter the server
          URL and use your key as the bearer token.
        </p>
        <Endpoint value={p.Endpoint} />
      </Disclosure>
    </>
  );
}
export function KeyReview({
  p,
}: {
  p: {
    Keys: AgentKey[];
    Admin: boolean;
    Path: string;
    CSRF: string;
    Review: string;
    Back: string;
  };
}) {
  return (
    <>
      <Head
        tag={p.Admin ? "Workspace keys" : "Your keys"}
        title="Review key revocation"
      />
      <Panel
        id="selected-keys"
        title={p.Keys.length + " selected " + plural(p.Keys.length, "key")}
      >
        <p>
          Agents using these keys will lose access immediately. You cannot
          restore a revoked key.
        </p>
        <ul className={reviewList}>
          {p.Keys.map((k) => (
            <li key={k.ID}>
              <strong>{k.Name}</strong>
              {p.Admin && (
                <>
                  {" "}
                  · Owner: <code>{k.Subject}</code>
                </>
              )}{" "}
              · ID: <code>{k.ID}</code>
            </li>
          ))}
        </ul>
        <Post
          path={p.Path}
          fields={{ action: "revoke-selected", csrf: p.CSRF, review: p.Review }}
          className={toolbar}
        >
          <ActionButton
            variant="destructive"
            name="confirm"
            value="yes"
            label={p.Keys.length === 1 ? "Revoke key" : "Revoke keys"}
            icon={Ban}
          />
          <ButtonLink href={p.Back} label="Cancel" icon={X} />
        </Post>
      </Panel>
    </>
  );
}
export function Quickstart({ p }: { p: QuickData }) {
  return (
    <>
      <Head tag={"Workspace · " + p.Workspace} title="Connect your agent">
        <p>
          Choose the app you use. We’ll create a key and give you a command to
          run on the computer where your agent runs.
        </p>
      </Head>
      <Post
        path="/quickstart"
        className={stack + " rounded-xl border bg-card p-4 sm:p-6"}
        fields={{ csrf: p.CSRF, creation_token: p.Creation }}
      >
        <Notice text={p.Err} error />
        <fieldset className="grid min-w-0 gap-3">
          <legend className="mb-3 text-lg font-semibold">
            Which agent do you use?
          </legend>
          <div className="grid min-w-0 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {p.Clients.map((c) => (
              <label
                key={c.ID}
                className="flex min-h-11 cursor-pointer items-center gap-2 rounded-md border p-3 has-checked:border-primary has-checked:bg-muted"
              >
                <input
                  type="radio"
                  name="client"
                  required
                  value={c.ID}
                  defaultChecked={p.Client === c.ID}
                  className="size-4 shrink-0 accent-primary"
                />
                <strong className="text-sm">{c.Name}</strong>
              </label>
            ))}
          </div>
          <p className={secondaryText}>
            The GitHub Copilot app, CLI and default VS Code Copilot agent share
            one local connection. If one is connected, the others are ready too.
            Running setup again changes the key for all three.
          </p>
        </fieldset>
        <div className="grid min-w-0 gap-4 md:grid-cols-2 xl:grid-cols-3">
          <Field id="quick-platform" label="Where will you run the command?">
            <Choices
              id="quick-platform"
              name="platform"
              defaultValue={p.Platform}
              choices={[
                ["unix", "macOS or Linux terminal"],
                ["windows", "Windows PowerShell"],
              ]}
            />
          </Field>
          <Field id="quick-scope" label="What can this agent do?">
            <Choices
              id="quick-scope"
              name="scope"
              defaultValue={p.Scope}
              required
              choices={p.Scopes.map((v) => [v, quickScopes[v]])}
            />
          </Field>
          <Field id="quick-days" label="Key lifetime">
            <Choices
              id="quick-days"
              name="days"
              defaultValue={p.Days}
              choices={lifetimes}
            />
          </Field>
        </div>
        <div className="space-y-1">
          <label className="inline-flex items-center gap-2 font-medium">
            <input
              type="checkbox"
              name="company_proxy"
              aria-describedby="proxy-help"
              defaultChecked={p.CompanyProxy}
            />
            Use a company proxy
          </label>
          <p className={secondaryText} id="proxy-help">
            Choose this if your computer needs a proxy to reach the internet.
            The installer uses your terminal’s proxy settings or asks for the
            address locally.
          </p>
        </div>
        <Disclosure title="Name this connection (optional)">
          <Field id="quick-name" label="Name in Agent keys">
            <Input
              id="quick-name"
              type="text"
              name="name"
              defaultValue={p.NameValue}
              maxLength={80}
              placeholder="e.g. My work laptop"
            />
          </Field>
        </Disclosure>
        <div>
          <ActionButton label="Create key and get command" icon={Terminal} />
        </div>
        <p className={secondaryText}>
          Does your agent run in the cloud or only accept a server URL?{" "}
          <a className={linkStyle} href="/keys">
            Set up a remote HTTP connection instead.
          </a>
        </p>
      </Post>
    </>
  );
}
const tryClient: Record<string, ReactNode> = {
  "copilot-app":
    "Open a local session in the GitHub Copilot app. In Customize, check that Shoowcase is enabled.",
  vscode:
    "Open a new session with the Copilot agent in VS Code. You can find the server in Agent Customizations → MCP Servers under Copilot Global.",
  "vscode-local":
    "In the default profile of VS Code Stable, start a session with the legacy Local agent. For custom profiles, use the Other local MCP client option. Run “MCP: List Servers” from the Command Palette to find Shoowcase.",
  stdio:
    "Add a server named shoowcase with the stdio transport and the command and arguments printed by the installer. Save the settings and restart the client.",
  gemini: "Start Gemini CLI and use /mcp to check the connection.",
  opencode: (
    <>
      Start a new OpenCode session. You can check the connection with{" "}
      <code>opencode mcp list</code>.
    </>
  ),
};
export function QuickReady({ p }: { p: QuickData }) {
  useReceipt(p, "Command", "/quickstart");
  const windows = p.Platform === "windows";
  return (
    <>
      <Head
        tag={"Quickstart · " + p.ClientName}
        title={"Connect " + p.ClientName}
      >
        <p>
          Your {role(p.Scope)} key is ready. It expires in {p.Days} days.
        </p>
      </Head>
      <Panel id="run-command" title="1. Run this command">
        {p.CompanyProxy && (
          <p>
            Your agent will start a local MCP relay that remembers how to reach
            Shoowcase through your proxy. If your IT team gave you a PEM
            certificate bundle, set <code>SHOOWCASE_CA_FILE</code> to its local
            path before running the command. Enter proxy details only in your
            terminal.
          </p>
        )}
        <p>
          Open {windows ? "PowerShell" : "a terminal"} on the computer where you
          use {p.ClientName}.{" "}
          {p.Client === "stdio"
            ? "This installs the connector and prints the command and arguments to enter in your client’s local MCP server settings."
            : "This installs the Shoowcase connector and registers it for your user."}
        </p>
        <Field
          id="setup-command"
          label={(windows ? "PowerShell" : "Terminal") + " command"}
          hintId="setup-expiry"
          hint="This command contains a private setup code. Use it once within 10 minutes. Don’t share it or paste it into chat."
        >
          <Textarea
            id="setup-command"
            className="min-w-0 break-all font-mono"
            data-secret-receipt
            readOnly
            rows={4}
            spellCheck={false}
            autoComplete="off"
            aria-describedby="setup-expiry"
            defaultValue={p.Command}
          />
          <Copy id="setup-command">Copy command</Copy>
        </Field>
      </Panel>
      <Panel id="first-connection" title={"2. Try it in " + p.ClientName}>
        <p>
          {tryClient[p.Client] ?? "Restart the agent or start a new session."}{" "}
          If asked to trust the Shoowcase connection, review and enable it. Then
          try this prompt:
        </p>
        <Field id="first-prompt" label="First prompt">
          <Input readOnly id="first-prompt" value={p.Prompt} />
          <Copy id="first-prompt">Copy prompt</Copy>
        </Field>
      </Panel>
      <p>
        Manage or revoke this key in{" "}
        <a className={linkStyle} href="/keys">
          Agent keys
        </a>
        . If your setup command expired, revoke the unused key there, then{" "}
        <a className={linkStyle} href="/quickstart">
          start again
        </a>
        .
      </p>
    </>
  );
}
