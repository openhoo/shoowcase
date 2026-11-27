import { ActionButton, ActionLink } from "@/components/icon-action";
import { PanelSection } from "@/components/panel-section";
import {
  Cell,
  NameCell,
  PickCell,
  RecordRow,
  RecordTable,
} from "@/components/record-table";
import { navigationTarget } from "@/lib/navigation";
import {
  Folder,
  ListFilter,
  ListRestart,
  Plus,
  Search,
  SlidersHorizontal,
  Trash2,
} from "lucide-react";

import { useState } from "react";

import {
  SelectAll,
  SelectRow,
  SelectionCount,
  useSelection,
} from "@/components/selection";
import { Input } from "@/components/ui/input";
import { siteListURL, type SiteData, type SiteListing } from "./sites-model";
import {
  ButtonLink,
  Empty,
  Field,
  FileIcon,
  Head,
  Hidden,
  NativeChoice,
  Notice,
  StateBadge,
  Token,
  linkStyle,
  sectionHeading,
  toolbar,
} from "./sites-ui";

const stateOptions = [
  ["", "All statuses"],
  ["draft", "Draft only"],
  ["changes", "Unpublished changes"],
  ["published", "Published"],
  ["offline", "Offline"],
] as const;
const audiences = [
  ["", "All audiences"],
  ["workspace", "Workspace"],
  ["public", "Public"],
] as const;
const sorts = [
  ["updated", "Last updated"],
  ["name", "Name A–Z"],
  ["oldest", "Oldest first"],
] as const;

function SiteFilters({ d, open }: { d: SiteListing; open: boolean }) {
  const f = d.Directory;
  return (
    <form
      action={d.InAdmin ? "/admin" : "/sites"}
      method="get"
      role="search"
      aria-label="Filter the site directory"
      className="grid items-end gap-3 md:grid-cols-[minmax(8rem,2fr)_minmax(0,5fr)_auto]"
    >
      {d.InAdmin && <Hidden name="view" value="sites" />}
      <Field id="site-search" label="Search sites">
        <span className="relative block">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground"
          />
          <Input
            className="pl-9"
            id="site-search"
            name="q"
            type="search"
            defaultValue={f.Query}
            maxLength={100}
            placeholder="Name or address"
          />
        </span>
      </Field>
      <div
        id="directory-options"
        className={
          (open ? "grid " : "hidden ") + "min-w-0 gap-3 md:grid md:grid-cols-3"
        }
      >
        {(
          [
            ["filter-state", "Status", "state", f.State, stateOptions],
            ["filter-audience", "Audience", "audience", f.Audience, audiences],
            ["sort-sites", "Sort by", "sort", f.Sort, sorts],
          ] as const
        ).map(([id, label, name, value, options]) => (
          <Field key={id} id={id} label={label}>
            <NativeChoice {...{ id, name, value, options }} />
          </Field>
        ))}
      </div>
      <ActionButton
        type="submit"
        variant="outline"
        label="Apply filters"
        icon={ListFilter}
      />
    </form>
  );
}

export function SiteDirectory({ d }: { d: SiteData }) {
  const f = d.Directory;
  const newSite = d.CanEdit && (
    <ButtonLink action primary href="/sites/new" label="New site" icon={Plus} />
  );
  return (
    <>
      <Head
        tag={"Publishing desk / " + d.C.WorkspaceLabel}
        title="Sites"
        action={newSite}
      >
        <p>Sites you created or that belong to one of your groups.</p>
      </Head>
      <dl
        className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border lg:grid-cols-4"
        aria-label="Site totals"
      >
        {(
          [
            ["Your sites", f.Total],
            ["Live sites", f.Published],
            ["Draft only", f.Drafts],
            ["Unpublished changes", f.Changes],
          ] as const
        ).map(([label, count]) => (
          <div key={label} className="space-y-1 bg-card px-4 py-2 sm:p-4">
            <dt className="text-sm text-muted-foreground">{label}</dt>
            <dd className="text-2xl font-semibold tabular-nums">{count}</dd>
          </div>
        ))}
      </dl>
      <Notice text={d.Notice} />
      <SiteDirectoryTable d={d} />
    </>
  );
}

export function SiteDirectoryTable({ d }: { d: SiteListing }) {
  const [open, setOpen] = useState(false),
    f = d.Directory,
    rows = f.Sites || [],
    canDeleteSites =
      d.C.Actor.Role === "admin" && rows.some((s) => s.DeletionValue);
  const sel = useSelection(rows.map((s) => s.DeletionValue).filter(Boolean));
  return (
    <PanelSection
      className="directory"
      aria-labelledby="directory-title"
      data-selection
    >
      <div className={toolbar}>
        <h2 className={sectionHeading} id="directory-title">
          {d.InAdmin ? "All sites" : "Site directory"}
        </h2>
        <span className="text-sm text-muted-foreground">
          {f.MatchCount} {f.MatchCount === 1 ? "site" : "sites"}
          {f.Filtered ? " of " + f.Total : ""}
        </span>
        <ActionButton
          className="md:hidden"
          type="button"
          variant="outline"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          aria-controls="directory-options"
          label="Filter and sort"
          icon={SlidersHorizontal}
        />
      </div>
      <SiteFilters d={d} open={open} />
      {f.Filtered && (
        <ActionLink
          variant="ghost"
          href={siteListURL(d.InAdmin)}
          label="Reset filters"
          icon={ListRestart}
        />
      )}
      {!rows.length ? (
        f.Total ? (
          <Empty title="No matching sites">
            Try another name or remove a filter.
          </Empty>
        ) : d.InAdmin ? (
          <Empty title="No sites in this workspace yet" />
        ) : (
          <Empty title="No sites yet">
            <p>
              Sites you create or that belong to one of your groups will appear
              here.
            </p>
            <a className={linkStyle} href="/quickstart">
              Connect your agent with Quickstart →
            </a>
          </Empty>
        )
      ) : (
        <>
          {canDeleteSites && (
            <>
              <form
                id="delete-sites-selection"
                method="post"
                action={siteListURL(d.InAdmin)}
              >
                <Token d={d} />
                <Hidden name="action" value="review-delete-sites" />
              </form>
              <div
                className={
                  toolbar +
                  " selection-toolbar rounded-lg bg-muted/50 p-3 text-sm"
                }
              >
                <SelectAll selection={sel} label="Select all on this page" />
                <SelectionCount selection={sel} />
                <ActionButton
                  type="submit"
                  variant="outline"
                  className="text-destructive"
                  form="delete-sites-selection"
                  disabled={!sel.count}
                  label="Delete selected sites…"
                  icon={Trash2}
                />
              </div>
              <p className="text-sm text-muted-foreground">
                Deletion removes the selected sites, including their live
                releases. You can review the selection before confirming.
              </p>
            </>
          )}
          <RecordTable
            id="directory-title"
            columns={[
              ...(canDeleteSites ? ["Select"] : []),
              "Site",
              "Status",
              "Audience",
              "Files",
              "Last updated",
              ...(canDeleteSites ? ["Actions"] : []),
            ]}
          >
            {rows.map((s) => (
              <RecordRow key={s.ID}>
                {canDeleteSites && (
                  <PickCell>
                    {s.DeletionValue && (
                      <SelectRow
                        selection={sel}
                        id={s.DeletionValue}
                        name="site"
                        label={`Select site ${s.Name} (${s.ID})`}
                        form="delete-sites-selection"
                      />
                    )}
                  </PickCell>
                )}
                <NameCell>
                  <div className="flex items-start gap-2">
                    <Folder
                      className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                    <div className="min-w-0">
                      <a className={linkStyle} href={navigationTarget(s.Href)}>
                        {s.Name}
                      </a>
                      <span className="block font-mono text-xs text-muted-foreground">
                        {s.Address}
                      </span>
                    </div>
                  </div>
                </NameCell>
                <Cell>
                  <StateBadge value={s.State} label={s.StateLabel} />
                </Cell>
                <Cell label="Audience">{s.AudienceLabel}</Cell>
                <Cell label="Files">
                  <span className="inline-flex items-center gap-1">
                    <FileIcon />
                    {s.FileCount}
                  </span>
                </Cell>
                <Cell label="Updated">
                  <time dateTime={s.Updated}>{s.UpdatedLabel}</time>
                </Cell>
                {canDeleteSites && (
                  <Cell>
                    {s.DeletionValue && (
                      <form method="post" action={siteListURL(d.InAdmin)}>
                        <Token d={d} />
                        <Hidden name="action" value="review-delete-sites" />
                        <ActionButton
                          variant="ghost"
                          className="text-destructive"
                          type="submit"
                          name="site"
                          value={s.DeletionValue}
                          label={`Delete site ${s.Name} (${s.ID})`}
                          icon={Trash2}
                        />
                      </form>
                    )}
                  </Cell>
                )}
              </RecordRow>
            ))}
          </RecordTable>
        </>
      )}
    </PanelSection>
  );
}
