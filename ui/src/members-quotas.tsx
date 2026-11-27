import { ActionButton, ActionLink } from "@/components/icon-action";
import { SelectRow, useSelection } from "@/components/selection";
import { Input } from "@/components/ui/input";
import { navigationTarget } from "@/lib/navigation";
import {
  RotateCcw,
  Save,
  SlidersHorizontal,
  Unlink,
  UsersRound,
  X,
} from "lucide-react";
import {
  type Budget,
  type MemberData,
  type Resource,
  type SiteQuota,
  plural,
  urlValue,
} from "./members-model";
import {
  BulkButton,
  BulkForm,
  Cell,
  Choices,
  DirectoryBox,
  Disclosure,
  Field,
  NameCell,
  Panel,
  PickCell,
  Post,
  RecordRow,
  RecordTable,
  secondaryText,
  stack,
} from "./members-ui";
import { ButtonLink, Empty, linkStyle, toolbar } from "./sites-ui";

const units = [
  ["GiB", 2 ** 30],
  ["MiB", 2 ** 20],
  ["KiB", 2 ** 10],
] as const;
function bytes(n: number) {
  for (const [name, size] of units)
    if (n >= size)
      return n % size === 0
        ? n / size + " " + name
        : (n / size).toFixed(1) + " " + name + " (" + n + " B)";
  return n + " B";
}
function Usage({ b, k }: { b: Budget; k: keyof Resource }) {
  const used = b.Usage[k],
    max = b.Effective[k],
    limit = b.Limits[k],
    fmt = k === "Bytes" ? bytes : String;
  return (
    <>
      {fmt(used)} of {fmt(max)}
      {!b.Workspace &&
        (limit < 0
          ? " · Workspace limit"
          : limit > max
            ? " · Group limit: " + fmt(limit)
            : " · Group limit")}
      {used >= max && (
        <>
          {" "}
          · <strong>{used > max ? "Over limit" : "At limit"}</strong>
        </>
      )}
    </>
  );
}
function BudgetEditor({ b, cap }: { b: Budget; cap: number }) {
  const key = b.Workspace ? "workspace" : b.ID;
  const prefix = (s: string) => s + "-" + key;
  return (
    <Post
      id={b.Workspace ? undefined : "budget-editor"}
      aria-labelledby={b.Workspace ? undefined : "budget-editor-title"}
      tabIndex={b.Workspace ? undefined : -1}
      className={stack}
      fields={{
        csrf: b.CSRF,
        version: b.Version,
        action: b.Workspace ? "workspace-quota" : "group-quota",
        group: b.Workspace ? undefined : b.ID,
      }}
    >
      {!b.Workspace && (
        <>
          <h3 id="budget-editor-title" className="font-semibold">
            Edit limits for {b.Name}
          </h3>
          <p>
            Leave either limit blank to use the workspace limit for that
            resource.
          </p>
        </>
      )}
      <div className="grid min-w-0 gap-4 sm:grid-cols-3">
        <Field id={prefix("site-limit")} label="Maximum sites">
          <Input
            id={prefix("site-limit")}
            type="number"
            min={0}
            step={1}
            max={cap}
            name="site_limit"
            defaultValue={b.SiteLimit}
            required={b.Workspace}
          />
        </Field>
        <Field
          id={prefix("storage-limit")}
          label="Maximum storage"
          hintId={prefix("storage-help")}
          hint="Enter a whole number and choose its unit."
        >
          <Input
            id={prefix("storage-limit")}
            type="number"
            min={0}
            step={1}
            name="storage_limit"
            defaultValue={b.StorageLimit}
            required={b.Workspace}
            aria-describedby={prefix("storage-help")}
          />
        </Field>
        <Field id={prefix("storage-unit")} label="Storage unit">
          <Choices
            id={prefix("storage-unit")}
            name="storage_unit"
            choices={[
              ["B", "Bytes"],
              ["KiB", "KiB"],
              ["MiB", "MiB"],
              ["GiB", "GiB"],
            ]}
            defaultValue={b.Unit}
          />
        </Field>
      </div>
      <div className={toolbar}>
        <ActionButton label="Save limits" icon={Save} />
        {!b.Workspace && (
          <ButtonLink
            href={"/admin?view=quotas#budget-" + urlValue(b.ID)}
            label="Cancel"
            icon={X}
          />
        )}
      </div>
    </Post>
  );
}
const selectId = (r: { ID: string; Version: number }) => r.ID + ":" + r.Version;
const hasLimit = (g: Budget) => g.Limits.Sites >= 0 || g.Limits.Bytes >= 0;
function ClearButton({
  p,
  action,
  row,
  label,
}: {
  p: MemberData;
  action: string;
  row: Budget | SiteQuota;
  label: string;
}) {
  return (
    <Post fields={{ csrf: p.CSRF, action }}>
      <ActionButton
        variant="outline"
        name="selected"
        value={selectId(row)}
        label={label}
        icon={action === "clear-group-limits" ? RotateCcw : Unlink}
      />
    </Post>
  );
}
function GroupQuotas({ p }: { p: MemberData }) {
  const q = p.Quota,
    groups = q.Groups ?? [],
    s = useSelection(groups.filter(hasLimit).map(selectId)),
    has = q.HasGroupLimits;
  return (
    <DirectoryBox
      id="group-quotas"
      title="Group quotas"
      count={groups.length + " " + plural(groups.length, "group")}
    >
      <div className="space-y-2">
        <p>
          Use a group to budget for a project. Each site can count toward one
          group. All groups share the available workspace capacity.
        </p>
        <p>
          Assign existing sites before setting the first group limit. Once a
          group has an explicit limit, only admins can add storage to unassigned
          sites.
        </p>
      </div>
      {q.EditBudget && <BudgetEditor b={q.EditBudget} cap={q.MaxSites} />}
      {has && (
        <BulkForm
          id="select-group-quotas"
          fields={{ csrf: p.CSRF, action: "clear-group-limits" }}
          selection={s}
        >
          <BulkButton
            disabled={!s.count}
            label="Clear selected limits"
            icon={RotateCcw}
          />
        </BulkForm>
      )}
      {groups.length > 0 ? (
        <RecordTable
          id="group-quotas"
          columns={[
            ...(has ? ["Select"] : []),
            "Group",
            "Sites",
            "Revision storage",
            "Actions",
          ]}
        >
          {groups.map((g) => (
            <RecordRow id={"budget-" + g.ID} key={g.ID}>
              {has && (
                <PickCell>
                  {hasLimit(g) ? (
                    <SelectRow
                      form="select-group-quotas"
                      selection={s}
                      id={selectId(g)}
                      label={"Select " + g.Name}
                    />
                  ) : (
                    <span aria-label="Not applicable">—</span>
                  )}
                </PickCell>
              )}
              <NameCell>
                <a
                  className={linkStyle}
                  href={"/admin?view=groups&group=" + urlValue(g.ID)}
                >
                  {g.Name}
                </a>
              </NameCell>
              <Cell label="Sites">
                <Usage b={g} k="Sites" />
              </Cell>
              <Cell label="Revision storage">
                <Usage b={g} k="Bytes" />
              </Cell>
              <Cell>
                <div className={toolbar}>
                  <ActionLink
                    variant="ghost"
                    href={
                      "/admin?view=quotas&budget=" +
                      urlValue(g.ID) +
                      "#budget-editor"
                    }
                    label={"Edit limits for " + g.Name}
                    icon={SlidersHorizontal}
                  />
                  {hasLimit(g) && (
                    <ClearButton
                      p={p}
                      row={g}
                      action="clear-group-limits"
                      label={"Clear limits for " + g.Name}
                    />
                  )}
                </div>
              </Cell>
            </RecordRow>
          ))}
        </RecordTable>
      ) : (
        <Empty title="No groups yet">
          <a className={linkStyle} href="/admin?view=groups">
            Create a group
          </a>{" "}
          to give a project its own limits.
        </Empty>
      )}
    </DirectoryBox>
  );
}
function Assignments({ p }: { p: MemberData }) {
  const q = p.Quota,
    sites = q.Sites ?? [],
    groups = q.Groups ?? [],
    s = useSelection(sites.filter((r) => r.GroupID).map(selectId)),
    has = q.HasAssignments,
    chosen = q.Selected;
  return (
    <DirectoryBox
      id="site-assignments"
      title="Site assignments"
      count={sites.length + " " + plural(sites.length, "site")}
    >
      <p>
        <strong>Unassigned:</strong> {q.Unassigned.Sites}{" "}
        {plural(q.Unassigned.Sites, "site")} · {bytes(q.Unassigned.Bytes)}.
        These sites count only toward the workspace limits.
      </p>
      {chosen && (
        <Post
          id="assignment"
          className={stack}
          fields={{
            csrf: p.CSRF,
            action: "site-group",
            site: chosen.ID,
            version: chosen.Version,
          }}
        >
          <h3 className="font-semibold">Change group for {chosen.Name}</h3>
          <p>
            Moving this site assigns its full revision history (
            {bytes(chosen.Bytes)}) to the new group. Its address and audience
            stay the same. If you choose a group, only its editors, its
            publishers and workspace admins can edit the draft.
          </p>
          <Field id="site-group" label={"Group for " + chosen.Name}>
            <Choices
              id="site-group"
              name="group"
              defaultValue={chosen.GroupID}
              choices={[
                ["", "Unassigned"],
                ...groups.map((g) => [g.ID, g.Name] as const),
              ]}
            />
          </Field>
          <div className={toolbar}>
            <ActionButton label="Save assignment" icon={Save} />
            <ButtonLink
              href="/admin?view=quotas#site-assignments"
              label="Cancel"
              icon={X}
            />
          </div>
        </Post>
      )}
      {has && (
        <BulkForm
          id="select-site-assignments"
          fields={{ csrf: p.CSRF, action: "clear-site-assignments" }}
          selection={s}
        >
          <BulkButton
            disabled={!s.count}
            label="Remove selected assignments"
            icon={Unlink}
          />
        </BulkForm>
      )}
      {sites.length > 0 ? (
        <RecordTable
          id="site-assignments"
          columns={[
            ...(has ? ["Select"] : []),
            "Site",
            "Group",
            "Revision storage",
            "Actions",
          ]}
        >
          {sites.map((site) => (
            <RecordRow key={site.ID}>
              {has && (
                <PickCell>
                  {site.GroupID ? (
                    <SelectRow
                      selection={s}
                      form="select-site-assignments"
                      id={selectId(site)}
                      label={"Select " + site.Name}
                    />
                  ) : (
                    <span aria-label="Not applicable">—</span>
                  )}
                </PickCell>
              )}
              <NameCell>
                <a className={linkStyle} href={navigationTarget(site.Href)}>
                  {site.Name}
                </a>
                <span className={"site-address block " + secondaryText}>
                  {site.ID}
                </span>
              </NameCell>
              <Cell label="Group">{site.GroupName}</Cell>
              <Cell label="Revision storage">{bytes(site.Bytes)}</Cell>
              <Cell>
                <div className={toolbar}>
                  <ActionLink
                    variant="ghost"
                    href={
                      "/admin?view=quotas&site=" +
                      urlValue(site.ID) +
                      "#assignment"
                    }
                    label={"Change group for " + site.Name}
                    icon={UsersRound}
                  />
                  {site.GroupID && (
                    <ClearButton
                      p={p}
                      row={site}
                      action="clear-site-assignments"
                      label={"Remove assignment for " + site.Name}
                    />
                  )}
                </div>
              </Cell>
            </RecordRow>
          ))}
        </RecordTable>
      ) : (
        <Empty title="No sites in this workspace yet." />
      )}
    </DirectoryBox>
  );
}
export function Quotas({ p }: { p: MemberData }) {
  const q = p.Quota;
  return (
    <>
      <Panel id="workspace-quotas" title="Workspace quotas">
        <p>
          Every site counts toward these limits. Storage includes every saved
          revision: a file kept in two revisions counts twice. Publishing an
          existing revision uses no extra storage.
        </p>
        <dl aria-label="Resource usage" className="grid gap-3 sm:grid-cols-2">
          {(["Sites", "Bytes"] as const).map((k) => (
            <div className="rounded-lg bg-muted/60 p-4" key={k}>
              <dt className={secondaryText}>
                {k === "Sites" ? "Sites" : "Revision storage"}
              </dt>
              <dd className="font-medium">
                <Usage b={q.Workspace} k={k} />
              </dd>
            </div>
          ))}
        </dl>
        <Disclosure title="Edit workspace limits">
          <BudgetEditor b={q.Workspace} cap={q.MaxSites} />
        </Disclosure>
        <p className={secondaryText}>
          The service allows up to {q.MaxSites} sites and {bytes(q.MaxBytes)}{" "}
          per workspace. Lowering a limit keeps existing sites and revisions. It
          blocks further growth for that resource until there is room.
        </p>
      </Panel>
      <GroupQuotas p={p} />
      <Assignments p={p} />
    </>
  );
}
