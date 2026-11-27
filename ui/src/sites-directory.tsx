import { navigationTarget } from "@/lib/navigation";
import { Folder, ListFilter, Plus, Search, Trash2 } from "lucide-react";
import {
  RecordTable,
  RecordRow,
  NameCell,
  Cell,
  PickCell,
} from "@/components/record-table";
import { PanelSection } from "@/components/panel-section";

import { useState } from "react";

import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  SelectAll,
  SelectRow,
  SelectionCount,
  useSelection,
} from "@/components/selection";
import type { SiteData, SiteEntry } from "./sites-model";
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
const target = (site: SiteEntry) => site.ID + ":" + site.DraftRevision;

function SiteFilters({ d, open }: { d: SiteData; open: boolean }) {
  const f = d.Directory;
  return (
    <form
      action="/sites"
      method="get"
      role="search"
      aria-label="Filter the site directory"
      className="grid items-end gap-3 md:grid-cols-[minmax(8rem,2fr)_minmax(0,5fr)_auto]"
    >
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
      <Button type="submit" variant="outline">
        <ListFilter aria-hidden="true" />
        Apply filters
      </Button>
    </form>
  );
}

export function SiteDirectory({ d }: { d: SiteData }) {
  const [open, setOpen] = useState(false),
    f = d.Directory,
    rows = f.Sites || [],
    canDeleteDrafts =
      d.C.Actor.Role === "admin" && rows.some((s) => s.DraftRevision);
  const sel = useSelection(rows.filter((s) => s.DraftRevision).map(target));
  const newSite = d.CanEdit && (
    <ButtonLink primary href="/sites/new">
      <Plus aria-hidden="true" />
      New site
    </ButtonLink>
  );
  return (
    <>
      <Head
        tag={"Publishing desk / " + d.C.WorkspaceLabel}
        title="Sites"
        action={newSite}
      />
      <dl
        className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border lg:grid-cols-4"
        aria-label="Workspace totals"
      >
        {(
          [
            ["All sites", f.Total],
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
      <PanelSection
        className="directory"
        aria-labelledby="directory-title"
        data-selection
      >
        <div className={toolbar}>
          <h2 className={sectionHeading} id="directory-title">
            Site directory
          </h2>
          <span className="text-sm text-muted-foreground">
            {f.MatchCount} {f.MatchCount === 1 ? "site" : "sites"}
            {f.Filtered ? " of " + f.Total : ""}
          </span>
          <Button
            className="md:hidden"
            type="button"
            variant="outline"
            onClick={() => setOpen(!open)}
            aria-expanded={open}
            aria-controls="directory-options"
          >
            <ListFilter aria-hidden="true" />
            Filter and sort
          </Button>
        </div>
        <SiteFilters d={d} open={open} />
        {f.Filtered && (
          <a className={linkStyle} href="/sites">
            Reset filters
          </a>
        )}
        {!rows.length ? (
          f.Total ? (
            <Empty title="No matching sites">
              Try another name or remove a filter.
            </Empty>
          ) : (
            <Empty title="No sites yet">
              <p>
                {d.CanEdit
                  ? "Create your first site to add files and preview a draft."
                  : "Sites created by your team will appear here."}
              </p>
              <a className={linkStyle} href="/quickstart">
                Connect your agent with Quickstart →
              </a>
            </Empty>
          )
        ) : (
          <>
            {canDeleteDrafts && (
              <>
                <form id="delete-sites-selection" method="post" action="/sites">
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
                  <Button
                    type="submit"
                    variant="outline"
                    className="text-destructive"
                    form="delete-sites-selection"
                    disabled={!sel.count}
                  >
                    <Trash2 aria-hidden="true" />
                    Delete selected drafts…
                  </Button>
                </div>
                <p className="text-sm text-muted-foreground">
                  Only never-published drafts can be deleted. To take a released
                  site offline, open its Access settings.
                </p>
              </>
            )}
            <RecordTable
              id="directory-title"
              columns={[
                ...(canDeleteDrafts ? ["Select"] : []),
                "Site",
                "Status",
                "Audience",
                "Files",
                "Last updated",
                ...(canDeleteDrafts ? ["Actions"] : []),
              ]}
            >
              {rows.map((s) => (
                <RecordRow key={s.ID}>
                  {canDeleteDrafts && (
                    <PickCell>
                      {s.DraftRevision && (
                        <SelectRow
                          selection={sel}
                          id={target(s)}
                          name="site"
                          label={`Select draft ${s.Name} (${s.ID})`}
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
                        <a
                          className={linkStyle}
                          href={navigationTarget(s.Href)}
                        >
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
                  {canDeleteDrafts && (
                    <Cell>
                      {s.DraftRevision ? (
                        <form method="post" action="/sites">
                          <Token d={d} />
                          <Hidden name="action" value="review-delete-sites" />
                          <Button
                            size="sm"
                            variant="ghost"
                            className="text-destructive"
                            type="submit"
                            name="site"
                            value={target(s)}
                            aria-label={`Delete draft ${s.Name} (${s.ID})`}
                          >
                            <Trash2 aria-hidden="true" />
                            Delete…
                          </Button>
                        </form>
                      ) : (
                        <span className="text-muted-foreground">
                          Release kept
                        </span>
                      )}
                    </Cell>
                  )}
                </RecordRow>
              ))}
            </RecordTable>
          </>
        )}
      </PanelSection>
    </>
  );
}
