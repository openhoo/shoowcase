import { ActionButton, ActionLink } from "@/components/icon-action";
import {
  Ban,
  ListFilter,
  ListRestart,
  RotateCcw,
  Save,
  Search,
  SlidersHorizontal,
  Trash2,
  UserRound,
  UserRoundMinus,
  UserRoundPlus,
  UsersRound,
} from "lucide-react";
import { useState, type ReactNode } from "react";

import { SelectRow, useSelection } from "@/components/selection";
import { Input } from "@/components/ui/input";
import { plural, role, urlValue, type MemberData } from "./members-model";
import {
  BulkButton,
  BulkForm,
  Cell,
  Choices,
  DateDisplay,
  DirectoryBox,
  Disclosure,
  Field,
  groupChoices,
  Hidden,
  NameCell,
  Pager,
  Panel,
  PickCell,
  Post,
  RecordRow,
  RecordTable,
  secondaryText,
  stack,
} from "./members-ui";
import { Empty, linkStyle, StateBadge, toolbar } from "./sites-ui";

const itemLink =
  "site-name inline-flex min-w-0 items-center gap-2 font-medium hover:underline";
const icon = "size-5 shrink-0 text-muted-foreground";
const formGrid = "grid min-w-0 gap-4 sm:grid-cols-2";
// Sites, members and groups keep their search and filters in the same order.
// Search stays visible on a small screen while the secondary controls collapse.
function SearchDirectory({
  p,
  children,
}: {
  p: MemberData;
  children: ReactNode;
}) {
  const d = p.Directory,
    members = p.View === "members",
    [open, setOpen] = useState(false);
  return (
    <DirectoryBox
      id="directory-title"
      title={members ? "Member directory" : "Group directory"}
      count={listCount(p, members ? "member" : "group")}
      control={
        <ActionButton
          type="button"
          className="md:hidden"
          variant="outline"
          aria-controls="directory-options"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          label="Filter and sort"
          icon={SlidersHorizontal}
        />
      }
    >
      <form
        method="get"
        action="/admin"
        role="search"
        aria-label={
          "Filter the " + (members ? "member" : "group") + " directory"
        }
        className="grid items-end gap-3 md:grid-cols-[minmax(8rem,2fr)_minmax(0,5fr)_auto]"
      >
        <Hidden fields={{ view: p.View }} />
        <Field id="directory-search" label={"Search " + p.View}>
          <span className="relative block">
            <Search
              className="pointer-events-none absolute top-2.5 left-3 size-4 text-muted-foreground"
              aria-hidden="true"
            />
            <Input
              id="directory-search"
              name="q"
              type="search"
              maxLength={100}
              defaultValue={d.Query}
              className="pl-9"
              placeholder={members ? "Name or sign-in ID" : "Group name"}
            />
          </span>
        </Field>
        <div
          id="directory-options"
          className={
            (open ? "grid " : "hidden ") +
            "min-w-0 gap-3 md:grid " +
            (members ? "md:grid-cols-3" : "md:grid-cols-2")
          }
        >
          {members && (
            <Field id="directory-state" label="Status">
              <Choices
                id="directory-state"
                name="state"
                defaultValue={d.State}
                choices={[
                  ["", "All statuses"],
                  ["active", "Active"],
                  ["suspended", "Suspended"],
                ]}
              />
            </Field>
          )}
          <Field id="directory-role" label="Role">
            <Choices
              id="directory-role"
              name="role"
              defaultValue={d.Role}
              choices={[
                ["", "All roles"],
                ...groupChoices,
                ...(members ? [["admin", "Admin"] as const] : []),
              ]}
            />
          </Field>
          <Field id="directory-sort" label="Sort by">
            <Choices
              id="directory-sort"
              name="sort"
              defaultValue={d.Sort}
              choices={[
                ["name", "Name A–Z"],
                members
                  ? ["subject", "Sign-in ID"]
                  : ["members", "Most members"],
              ]}
            />
          </Field>
        </div>
        <ActionButton
          type="submit"
          variant="outline"
          label="Apply filters"
          icon={ListFilter}
        />
      </form>
      {(d.Filtered || d.Sort !== "name") && (
        <ActionLink
          variant="ghost"
          href={
            p.View === "groups" ? "/admin?view=groups" : "/admin?view=members"
          }
          label="Reset filters"
          icon={ListRestart}
        />
      )}
      {children}
    </DirectoryBox>
  );
}
function RowAction({
  csrf,
  id,
  group,
  operation,
  label,
}: {
  csrf: string;
  id: string;
  group?: string;
  operation: string;
  label: string;
}) {
  return (
    <Post fields={{ csrf, action: "review-selection", group, selected: id }}>
      <ActionButton
        type="submit"
        variant="outline"
        name="operation"
        value={operation}
        label={label}
        icon={
          operation === "restore-members"
            ? RotateCcw
            : operation === "delete-groups"
              ? Trash2
              : operation === "remove-members"
                ? UserRoundMinus
                : Ban
        }
      />
    </Post>
  );
}
function listCount(p: MemberData, noun: string) {
  const d = p.Directory;
  return (
    d.Matches +
    (d.Filtered ? " of " + d.Total : "") +
    " " +
    plural(d.Total, noun)
  );
}
function CreateMember({ p }: { p: MemberData }) {
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {p.LocalAdmin && (
        <Disclosure title="Create a password account">
          <Post
            className={stack}
            fields={{ csrf: p.CSRF, action: "create-local" }}
          >
            <Field
              id="new-username"
              label="Username"
              hint="Use 3–64 letters, numbers, periods, underscores or hyphens. Start with a letter or number."
            >
              <Input
                id="new-username"
                required
                name="username"
                autoComplete="off"
                spellCheck={false}
                minLength={3}
                maxLength={64}
                aria-describedby="new-username-help"
              />
            </Field>
            <Field
              id="starting-password"
              label="Starting password"
              hint="Use at least 12 characters. Share the password privately. The member can change it under Account."
            >
              <Input
                required
                id="starting-password"
                type="password"
                name="password"
                minLength={12}
                autoComplete="new-password"
                aria-describedby="starting-password-help"
              />
            </Field>
            <Field id="local-role" label="Sign-in role">
              <Choices
                id="local-role"
                name="role"
                choices={groupChoices}
                defaultValue="reader"
              />
            </Field>
            <div>
              <ActionButton
                label="Create password account"
                icon={UserRoundPlus}
              />
            </div>
          </Post>
        </Disclosure>
      )}
      <Disclosure title="Add a member">
        <Post className={stack} fields={{ csrf: p.CSRF, action: "provision" }}>
          <p className={secondaryText}>
            Use the person’s exact subject ID from your identity provider. They
            must sign in to this workspace to use the account.
          </p>
          <Field id="member-subject" label="Sign-in ID">
            <Input
              id="member-subject"
              name="subject"
              required
              maxLength={255}
            />
          </Field>
          <Field id="member-name" label="Display name">
            <Input
              id="member-name"
              name="display_name"
              required
              maxLength={80}
            />
          </Field>
          <div>
            <ActionButton label="Add member" icon={UserRoundPlus} />
          </div>
        </Post>
      </Disclosure>
    </div>
  );
}
function MemberDetails({ p }: { p: MemberData }) {
  const m = p.Member!;
  const guard = {
    csrf: p.CSRF,
    subject: m.Subject,
    expected_ceiling: m.Ceiling,
    expected_disabled: m.Disabled,
  };
  return (
    <>
      <a href="/admin" className={linkStyle}>
        ← All members
      </a>
      <Panel id="member-title" title={m.DisplayName}>
        <div className={toolbar}>
          <UserRound aria-hidden="true" className={icon} />
          <code>{m.Subject}</code>
          <StateBadge
            value={m.Disabled ? "offline" : "published"}
            label={m.Disabled ? "Suspended" : "Active"}
          />
          <span>{role(m.Role)}</span>
        </div>
        {m.Manage ? (
          <>
            <p>
              Sign-in role: {role(m.IdentityRole)}.{" "}
              {m.IdentityRole === "admin"
                ? "Your identity provider manages this role."
                : "Role limit: " + role(m.Ceiling) + "."}
            </p>
            {m.IdentityRole !== "admin" && (
              <Post className={stack} fields={{ ...guard, action: "role" }}>
                <Field
                  id="ceiling-member"
                  label={"Role limit for " + m.DisplayName}
                  hintId="role-help-member"
                  hint="A limit can only reduce the access granted at sign-in."
                >
                  <Choices
                    id="ceiling-member"
                    name="ceiling"
                    defaultValue={m.Ceiling}
                    choices={[
                      ["", "Use sign-in role"],
                      ...(m.Ceilings ?? []).map((v) => [v, role(v)] as const),
                    ]}
                    aria-describedby="role-help-member"
                  />
                </Field>
                <div>
                  <ActionButton label="Save role limit" icon={Save} />
                </div>
              </Post>
            )}
            <Post
              className={stack + " border-t pt-4"}
              fields={{ ...guard, action: m.Disabled ? "restore" : "suspend" }}
            >
              {m.Disabled ? (
                <p>
                  Restoring access lets this member sign in again. Their revoked
                  keys stay inactive.
                </p>
              ) : (
                <label>
                  <input
                    className="mr-2"
                    type="checkbox"
                    name="confirm"
                    value="yes"
                    required
                  />
                  Suspend this member and revoke all their keys.
                </label>
              )}
              <div>
                <ActionButton
                  variant={m.Disabled ? "default" : "destructive"}
                  label={m.Disabled ? "Restore access" : "Suspend member"}
                  icon={m.Disabled ? RotateCcw : Ban}
                />
              </div>
            </Post>
          </>
        ) : (
          <p>
            This is your account.{" "}
            <a href="/account" className={linkStyle}>
              Open account settings
            </a>
          </p>
        )}
      </Panel>
    </>
  );
}
function MemberDirectory({ p }: { p: MemberData }) {
  const rows = p.Members ?? [],
    s = useSelection(
      rows.filter((m) => m.Subject !== p.Subject).map((m) => m.Subject),
    );
  const kinds = new Set(
    rows
      .filter((m) => s.selected.has(m.Subject))
      .map((m) => (m.Disabled ? "suspended" : "active")),
  );
  return (
    <>
      <CreateMember p={p} />
      <SearchDirectory p={p}>
        {rows.length > 0 ? (
          <>
            <BulkForm
              id="member-selection"
              selection={s}
              detail={
                kinds.size > 1
                  ? ". Select only active or only suspended members to continue."
                  : undefined
              }
              fields={{ csrf: p.CSRF, action: "review-selection" }}
            >
              <BulkButton
                name="operation"
                value="suspend-members"
                disabledHint="Select only active members."
                data-requires-kind="active"
                disabled={kinds.size !== 1 || !kinds.has("active")}
                label="Suspend selected"
                icon={UserRoundMinus}
              />
              <BulkButton
                name="operation"
                value="restore-members"
                disabledHint="Select only suspended members."
                data-requires-kind="suspended"
                disabled={kinds.size !== 1 || !kinds.has("suspended")}
                label="Restore selected"
                icon={RotateCcw}
              />
            </BulkForm>
            <RecordTable
              id="directory-title"
              columns={[
                "Select",
                "Member",
                "Status",
                "Role",
                "Joined",
                "Action",
              ]}
            >
              {rows.map((m) => {
                const self = m.Subject === p.Subject;
                return (
                  <RecordRow key={m.Subject}>
                    <PickCell>
                      {self ? (
                        "—"
                      ) : (
                        <SelectRow
                          selection={s}
                          id={m.Subject}
                          kind={m.Disabled ? "suspended" : "active"}
                          form="member-selection"
                          label={
                            "Select " + m.DisplayName + " (" + m.Subject + ")"
                          }
                        />
                      )}
                    </PickCell>
                    <NameCell>
                      <div className="flex min-w-0 items-center gap-2">
                        <UserRound className={icon} aria-hidden="true" />
                        <div className="min-w-0">
                          <a
                            className={itemLink}
                            href={
                              self
                                ? "/account"
                                : "/admin?view=members&member=" +
                                  urlValue(m.Subject)
                            }
                          >
                            {m.DisplayName}
                          </a>
                          <span
                            className={"site-address block " + secondaryText}
                          >
                            {m.Subject}
                            {self ? " · Your account" : ""}
                          </span>
                        </div>
                      </div>
                    </NameCell>
                    <Cell label="Status">
                      <StateBadge
                        value={m.Disabled ? "offline" : "published"}
                        label={m.Disabled ? "Suspended" : "Active"}
                      />
                    </Cell>
                    <Cell label="Role">{role(m.Role)}</Cell>
                    <Cell label="Joined">
                      <DateDisplay value={m.CreatedAt} />
                    </Cell>
                    <Cell>
                      {self ? (
                        "—"
                      ) : (
                        <RowAction
                          csrf={p.CSRF}
                          id={m.Subject}
                          operation={
                            m.Disabled ? "restore-members" : "suspend-members"
                          }
                          label={
                            (m.Disabled ? "Restore " : "Suspend ") +
                            m.DisplayName
                          }
                        />
                      )}
                    </Cell>
                  </RecordRow>
                );
              })}
            </RecordTable>
          </>
        ) : (
          <Empty
            title={
              p.Directory.Filtered
                ? "No matching members"
                : "No members on this page"
            }
          >
            {p.Directory.Filtered
              ? "Try another name or sign-in ID, or reset the filters."
              : null}
          </Empty>
        )}
        <Pager
          label="Member pages"
          noun="members"
          prev={p.Directory.Prev}
          next={p.Directory.Next}
        />
      </SearchDirectory>
    </>
  );
}
export function MemberListing({ p }: { p: MemberData }) {
  return p.Member ? <MemberDetails p={p} /> : <MemberDirectory p={p} />;
}

function GroupDirectory({ p }: { p: MemberData }) {
  const rows = p.Groups ?? [],
    s = useSelection(rows.map((g) => g.ID)),
    d = p.Directory;
  return (
    <>
      <p className={secondaryText}>
        {p.Settings.Mode === "managed" ? (
          "Groups grant access in this workspace. A person in several groups gets the highest group role allowed by their account."
        ) : (
          <>
            Group grants are inactive while the workspace uses identity provider
            roles. Prepare groups here, then switch to managed access in{" "}
            <a className={linkStyle} href="/admin?view=settings">
              workspace settings
            </a>
            .
          </>
        )}
      </p>
      <Disclosure title="Create a group" open={d.Total === 0}>
        <Post
          className={stack}
          fields={{ csrf: p.CSRF, action: "group-create" }}
        >
          <div className={formGrid}>
            <Field id="new-group-name" label="Group name">
              <Input required maxLength={80} name="name" id="new-group-name" />
            </Field>
            <Field id="new-group-role" label="Group role">
              <Choices
                id="new-group-role"
                name="role"
                choices={groupChoices}
                defaultValue="reader"
              />
            </Field>
          </div>
          <div>
            <ActionButton label="Create group" icon={UsersRound} />
          </div>
        </Post>
      </Disclosure>
      <SearchDirectory p={p}>
        {rows.length > 0 ? (
          <>
            <BulkForm
              id="group-selection"
              fields={{ csrf: p.CSRF, action: "review-selection" }}
              selection={s}
            >
              <BulkButton
                name="operation"
                value="delete-groups"
                disabled={!s.count}
                label="Delete selected"
                icon={Trash2}
              />
            </BulkForm>
            <RecordTable
              id="directory-title"
              columns={["Select", "Group", "Role", "Members", "Action"]}
            >
              {rows.map((g) => (
                <RecordRow key={g.ID}>
                  <PickCell>
                    <SelectRow
                      selection={s}
                      id={g.ID}
                      label={"Select " + g.Name}
                      form="group-selection"
                    />
                  </PickCell>
                  <NameCell>
                    <a
                      className={itemLink}
                      href={"/admin?view=groups&group=" + urlValue(g.ID)}
                    >
                      <UsersRound aria-hidden="true" className={icon} />
                      {g.Name}
                    </a>
                  </NameCell>
                  <Cell label="Role">{role(g.Role)}</Cell>
                  <Cell label="Members">{g.Members}</Cell>
                  <Cell>
                    <RowAction
                      csrf={p.CSRF}
                      id={g.ID}
                      operation="delete-groups"
                      label={"Delete " + g.Name}
                    />
                  </Cell>
                </RecordRow>
              ))}
            </RecordTable>
          </>
        ) : (
          <Empty title={d.Total ? "No matching groups" : "No groups yet."}>
            {d.Total
              ? "Try another name or reset the filters."
              : "Create a group to grant workspace roles to its members."}
          </Empty>
        )}
        <Pager label="Group pages" noun="groups" prev={d.Prev} next={d.Next} />
      </SearchDirectory>
    </>
  );
}
function GroupDetails({ p }: { p: MemberData }) {
  const g = p.Group!,
    rows = p.GroupMembers ?? [],
    s = useSelection(rows),
    fields = { csrf: p.CSRF, group: g.ID, version: g.Version };
  return (
    <>
      <a className={linkStyle} href="/admin?view=groups">
        ← All groups
      </a>
      <header className={stack}>
        <div>
          <p className={secondaryText}>Workspace group</p>
          <h2 className="text-2xl font-semibold" id="selected-group">
            {g.Name}
          </h2>
        </div>
        <p>
          {role(g.Role)} ·{" "}
          <a
            className={linkStyle}
            href={"/admin?view=quotas#budget-" + urlValue(g.ID)}
          >
            View this group’s usage and limits
          </a>
        </p>
      </header>
      <DirectoryBox
        id="group-roster"
        title="Group members"
        count={p.Directory.Total + " " + plural(p.Directory.Total, "member")}
      >
        {rows.length > 0 ? (
          <>
            <BulkForm
              id="roster-selection"
              fields={{ csrf: p.CSRF, group: g.ID, action: "review-selection" }}
              selection={s}
            >
              <BulkButton
                name="operation"
                value="remove-members"
                disabled={!s.count}
                label="Remove selected"
                icon={UserRoundMinus}
              />
            </BulkForm>
            <RecordTable
              id="group-roster"
              columns={["Select", "Member sign-in ID", "Membership"]}
            >
              {rows.map((id) => (
                <RecordRow key={id}>
                  <PickCell>
                    <SelectRow
                      selection={s}
                      id={id}
                      label={"Select " + id}
                      form="roster-selection"
                    />
                  </PickCell>
                  <NameCell>
                    <a
                      className={itemLink}
                      href={"/admin?view=members&member=" + urlValue(id)}
                    >
                      <UserRound aria-hidden="true" className={icon} />
                      {id}
                    </a>
                  </NameCell>
                  <Cell>
                    <RowAction
                      id={id}
                      group={g.ID}
                      csrf={p.CSRF}
                      operation="remove-members"
                      label={"Remove " + id + " from group"}
                    />
                  </Cell>
                </RecordRow>
              ))}
            </RecordTable>
          </>
        ) : (
          <Empty
            title={
              p.Directory.Total
                ? "No members on this page."
                : "This group has no members."
            }
          />
        )}
        <Pager
          label="Group member pages"
          noun="members"
          prev={p.Directory.Prev}
          next={p.Directory.Next}
        />
      </DirectoryBox>
      <Panel id="manage-group" title="Manage group">
        <Post
          fields={{ ...fields, action: "group-add-member" }}
          className={stack}
        >
          <Field
            id="group-member"
            label="Member sign-in ID"
            hintId="subject-help"
            hint={
              <>
                Use the sign-in ID shown under{" "}
                <a className={linkStyle} href="/admin">
                  Members
                </a>
                .
              </>
            }
          >
            <Input
              id="group-member"
              name="subject"
              maxLength={255}
              required
              aria-describedby="subject-help"
            />
          </Field>
          <div>
            <ActionButton label="Add to group" icon={UserRoundPlus} />
          </div>
        </Post>
        <Disclosure title="Edit group">
          <Post
            fields={{ ...fields, action: "group-update" }}
            className={stack}
          >
            <div className={formGrid}>
              <Field id="group-name" label="Current group name">
                <Input
                  id="group-name"
                  name="name"
                  defaultValue={g.Name}
                  maxLength={80}
                  required
                />
              </Field>
              <Field id="group-role" label="Current group role">
                <Choices
                  id="group-role"
                  name="role"
                  choices={groupChoices}
                  defaultValue={g.Role}
                />
              </Field>
            </div>
            <div>
              <ActionButton label="Save group" icon={Save} />
            </div>
          </Post>
        </Disclosure>
        <Disclosure title="Delete group">
          <p>
            Move any sites assigned to this group in{" "}
            <a className={linkStyle} href="/admin?view=quotas#site-assignments">
              Quotas
            </a>{" "}
            before deleting it.
          </p>
          <Post
            fields={{ ...fields, action: "group-delete" }}
            className={stack}
          >
            <label>
              <input
                type="checkbox"
                className="mr-2"
                required
                name="confirm"
                value="yes"
              />
              Remove this group and all its grants.
            </label>
            <div>
              <ActionButton
                variant="destructive"
                label="Delete group"
                icon={Trash2}
              />
            </div>
          </Post>
        </Disclosure>
      </Panel>
    </>
  );
}
export function Groups({ p }: { p: MemberData }) {
  return p.Group ? <GroupDetails p={p} /> : <GroupDirectory p={p} />;
}
