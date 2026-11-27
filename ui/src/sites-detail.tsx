import { ActionButton, ActionLink } from "@/components/icon-action";
import { PanelSection } from "@/components/panel-section";
import {
  Cell,
  NameCell,
  PickCell,
  RecordRow,
  RecordTable,
} from "@/components/record-table";
import { postTarget } from "@/lib/navigation";
import {
  Archive,
  CloudUpload,
  Download,
  ExternalLink,
  Eye,
  History,
  Plus,
  ShieldCheck,
  Trash2,
  Upload,
} from "lucide-react";

import {
  SelectAll,
  SelectRow,
  SelectionCount,
  exportCSV,
  useSelection,
} from "@/components/selection";
import {
  audienceName,
  humanBytes,
  shortID,
  utcTime,
  type SiteData,
} from "./sites-model";
import {
  Back,
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
  Write,
  linkStyle,
  sectionHeading,
  toolbar,
} from "./sites-ui";

const editable = (path: string) => /\.(?:html|css|js)$/i.test(path);
function DraftFiles({ d }: { d: SiteData }) {
  const rows = d.S?.Files || [],
    sel = useSelection(rows.map((f) => f.path));
  return (
    <PanelSection
      className="directory"
      aria-labelledby="draft-title"
      data-selection
    >
      <div className={toolbar}>
        <h2 className={sectionHeading} id="draft-title">
          Draft files
        </h2>
        <span className="text-sm text-muted-foreground">
          {rows.length} {rows.length === 1 ? "file" : "files"}
        </span>
      </div>
      {d.S?.StorageRestriction && (
        <p className="rounded-md border bg-muted/50 p-3 text-sm">
          {d.S.StorageRestriction}
        </p>
      )}
      {d.CanEdit && (
        <div className={toolbar}>
          <ButtonLink
            action
            primary
            href={d.Base + "/edit"}
            label="New file"
            icon={Plus}
          />
          <ButtonLink
            action
            href={d.Base + "/upload"}
            label="Upload file"
            icon={Upload}
          />
          <ButtonLink
            action
            href={d.Base + "/import"}
            label="Import ZIP"
            icon={Archive}
          />
        </div>
      )}
      {rows.length > 0 ? (
        <>
          {d.CanEdit && (
            <>
              <form
                id="delete-files-selection"
                action={postTarget(d.Base + "/delete")}
                method="post"
              >
                <Write d={d} />
                <Hidden name="step" value="review" />
              </form>
              <div className={toolbar}>
                <SelectAll selection={sel} label="Select all on this page" />
                <SelectionCount selection={sel} />
                <ActionButton
                  type="submit"
                  variant="outline"
                  className="text-destructive"
                  form="delete-files-selection"
                  disabled={!sel.count}
                  label="Remove selected files…"
                  icon={Trash2}
                />
              </div>
            </>
          )}
          <RecordTable
            id="draft-title"
            columns={
              d.CanEdit
                ? ["Select", "File", "Size", "Actions"]
                : ["File", "Size"]
            }
          >
            {rows.map((f) => (
              <RecordRow key={f.path}>
                {d.CanEdit && (
                  <PickCell>
                    <SelectRow
                      selection={sel}
                      id={f.path}
                      label={"Select file " + f.path}
                      name="path"
                      form="delete-files-selection"
                    />
                  </PickCell>
                )}
                <NameCell>
                  <div className="flex items-center gap-2">
                    <FileIcon />
                    {d.CanEdit && editable(f.path) ? (
                      <a
                        className={linkStyle}
                        href={
                          d.Base + "/edit?path=" + encodeURIComponent(f.path)
                        }
                      >
                        <code>{f.path}</code>
                      </a>
                    ) : (
                      <code>{f.path}</code>
                    )}
                  </div>
                </NameCell>
                <Cell label="Size">{humanBytes(f.size)}</Cell>
                {d.CanEdit && (
                  <Cell>
                    <ActionLink
                      variant="ghost"
                      className="text-destructive"
                      href={
                        d.Base + "/delete?path=" + encodeURIComponent(f.path)
                      }
                      label={"Remove " + f.path}
                      icon={Trash2}
                    />
                  </Cell>
                )}
              </RecordRow>
            ))}
          </RecordTable>
          {!d.HasIndex && (
            <p className="rounded-md border bg-muted/50 p-3 text-sm">
              Add a file named <code>index.html</code> before publishing. It is
              the page visitors open first.
            </p>
          )}
        </>
      ) : (
        <Empty title="No files yet.">
          {d.CanEdit && (
            <>
              Create <code>index.html</code> in the editor or import a ZIP with
              your site files.
            </>
          )}
        </Empty>
      )}
    </PanelSection>
  );
}

function SiteAccess({ d }: { d: SiteData }) {
  const s = d.S!,
    options: [string, string][] = [["workspace", "Workspace"]];
  if (d.AllowPublic && s.ReleaseID) options.push(["public", "Public"]);
  options.push(["offline", "Offline"]);
  return (
    <PanelSection aria-labelledby="audience-title">
      <h2 className={sectionHeading} id="audience-title">
        Access
      </h2>
      <p>
        {s.Visibility === "public"
          ? "Anyone with the address can open this site."
          : s.Visibility === "offline"
            ? "The live address is offline. Workspace members can still open draft previews."
            : "Only signed-in workspace members can open this site."}
      </p>
      {s.ConfiguredVisibility === "public" && !d.AllowPublic && (
        <p>
          Public sharing is disabled on this server. Set this site to Workspace
          to keep it private if public sharing is enabled later.
        </p>
      )}
      {d.CanPublish && (
        <form
          method="post"
          action={postTarget(d.Base + "/access")}
          className="flex flex-wrap items-end gap-3"
        >
          <Token d={d} />
          <Hidden name="request_id" value={d.Request + "-access"} />
          <Hidden name="revision" value={s.LiveRevision || d.Rev} />
          <Field id="visibility" label="Site audience">
            <NativeChoice
              id="visibility"
              name="visibility"
              value={s.Visibility}
              options={options}
            />
          </Field>
          <ActionButton
            type="submit"
            variant="outline"
            label="Update access"
            icon={ShieldCheck}
          />
        </form>
      )}
    </PanelSection>
  );
}

function Releases({ d }: { d: SiteData }) {
  const rs = d.History?.Releases || [],
    sel = useSelection(rs.map((r) => r.release_id)),
    current = d.S?.ReleaseID;
  const header = ["Revision", "Released", "Status"];
  function download() {
    const rows = rs
      .filter((r) => sel.selected.has(r.release_id))
      .map((r) => [
        shortID(r.revision),
        utcTime(r.created_at),
        r.release_id === current ? "Current" : "Previous",
      ]);
    exportCSV("releases.csv", header, rows);
  }
  return (
    <PanelSection
      id="releases"
      className="directory"
      aria-labelledby="releases-title"
      data-selection
    >
      <h2 className={sectionHeading} id="releases-title">
        Releases
      </h2>
      {rs.length ? (
        <>
          <div className={toolbar}>
            <SelectAll selection={sel} label="Select all on this page" />
            <SelectionCount selection={sel} />
            <ActionButton
              type="button"
              variant="outline"
              onClick={download}
              disabled={!sel.count}
              label="Download selected (CSV)"
              icon={Download}
            />
          </div>
          <RecordTable
            id="releases-title"
            columns={[
              "Select",
              ...header,
              ...(d.CanPublish ? ["Actions"] : []),
            ]}
          >
            {rs.map((r) => (
              <RecordRow key={r.release_id}>
                <PickCell>
                  <SelectRow
                    selection={sel}
                    name="release"
                    id={r.release_id}
                    label={`Select release ${shortID(r.release_id)}, ${utcTime(r.created_at)}`}
                  />
                </PickCell>
                <NameCell>
                  <code>{shortID(r.revision)}</code>
                </NameCell>
                <Cell label="Released">
                  <time dateTime={r.created_at}>{utcTime(r.created_at)}</time>
                </Cell>
                <Cell label="Status">
                  {r.release_id === current ? (
                    <StateBadge value="published" label="Current" />
                  ) : (
                    "Previous"
                  )}
                </Cell>
                {d.CanPublish && (
                  <Cell>
                    {r.release_id !== current && (
                      <ActionLink
                        variant="ghost"
                        href={
                          d.Base +
                          "/rollback?release=" +
                          encodeURIComponent(r.release_id)
                        }
                        label={
                          "Review restore of revision " + shortID(r.revision)
                        }
                        icon={History}
                      />
                    )}
                  </Cell>
                )}
              </RecordRow>
            ))}
          </RecordTable>
          {d.History?.NextCursor && (
            <nav aria-label="Release pages">
              <a
                className={linkStyle}
                href={
                  d.Base +
                  "?releases=" +
                  encodeURIComponent(d.History.NextCursor) +
                  "#releases"
                }
              >
                Older releases
              </a>
            </nav>
          )}
        </>
      ) : (
        <Empty title="No releases yet." />
      )}
    </PanelSection>
  );
}

export function SiteDetail({ d }: { d: SiteData }) {
  const s = d.S!;
  const state = !s.ReleaseID
    ? "Unpublished"
    : s.Visibility === "offline"
      ? "Offline"
      : s.LiveRevision === s.Revision
        ? "Published"
        : "Unpublished changes";
  return (
    <>
      <Back />
      <Head tag="Site" title={s.Name}>
        <p className="text-muted-foreground">
          {state} <span aria-hidden="true">·</span> {audienceName(s.Visibility)}
        </p>
      </Head>
      <div className={toolbar}>
        {d.HasIndex && (
          <ButtonLink
            external
            href={d.PreviewURL}
            label="Preview draft"
            icon={Eye}
          />
        )}
        {s.ReleaseID && s.Visibility !== "offline" && (
          <ButtonLink
            external
            href={d.SiteURL}
            label="Open live site"
            icon={ExternalLink}
          />
        )}
        {d.CanPublish && d.HasIndex && s.LiveRevision !== s.Revision && (
          <form method="post" action={postTarget(d.Base + "/publish")}>
            <Write d={d} />
            <ActionButton
              type="submit"
              label="Publish draft"
              icon={CloudUpload}
            />
          </form>
        )}
        {d.DeletionValue && d.C.Actor.Role === "admin" && (
          <form method="post" action="/sites">
            <Token d={d} />
            <Hidden name="action" value="review-delete-sites" />
            <ActionButton
              type="submit"
              variant="outline"
              className="text-destructive"
              name="site"
              value={d.DeletionValue}
              label="Delete site"
              icon={Trash2}
            />
          </form>
        )}
      </div>
      <Notice text={d.Notice} />
      <DraftFiles d={d} />
      <SiteAccess d={d} />
      <Releases d={d} />
    </>
  );
}
