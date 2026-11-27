import { ActionButton } from "@/components/icon-action";
import { PanelSection } from "@/components/panel-section";
import { navigationTarget, postTarget } from "@/lib/navigation";
import { FileUp, FolderPlus, History, Save, Trash2, X } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { SiteData } from "./sites-model";
import {
  Back,
  ButtonLink,
  Field,
  Head,
  Hidden,
  NativeChoice,
  Notice,
  Token,
  Write,
  linkStyle,
  toolbar,
} from "./sites-ui";

export function CreateSite({ d }: { d: SiteData }) {
  const groups = d.Creation.Groups || [],
    unassigned = d.Creation.AllowUnassigned;
  const choices: [string, string][] = [];
  if (unassigned) choices.push(["", "Workspace only (unassigned)"]);
  else if (groups.length > 1) choices.push(["", "Choose a group"]);
  for (const g of groups) choices.push([g.id, g.name]);
  return (
    <>
      <Back />
      <Head tag="Publishing desk" title="New site">
        <p>
          Start with a name. You can add a ZIP, upload files or write your first
          page next.
        </p>
      </Head>
      <Notice text={d.Error} error />
      <PanelSection aria-label="New site details">
        {groups.length || unassigned ? (
          <form className="grid max-w-2xl gap-5" method="post" action="/sites">
            <Write d={d} />
            <Field id="site-name" label="Site name">
              <Input
                id="site-name"
                name="name"
                required
                maxLength={100}
                defaultValue={d.Name}
                placeholder="Team handbook"
              />
            </Field>
            {groups.length > 0 && (
              <Field
                id="site-group"
                label="Budget group"
                hint="The site and its saved revisions count toward this budget and the workspace total."
              >
                <NativeChoice
                  name="group_id"
                  id="site-group"
                  value={d.GroupID}
                  required={!unassigned}
                  hint
                  options={choices}
                />
              </Field>
            )}
            <Field
              id="site-slug"
              label={
                <>
                  Site address{" "}
                  <span className="text-muted-foreground">(optional)</span>
                </>
              }
              hint="Use lowercase letters, numbers and hyphens, or leave blank for an automatic address."
            >
              <Input
                id="site-slug"
                name="slug"
                maxLength={48}
                autoCapitalize="none"
                spellCheck={false}
                aria-describedby="site-slug-hint"
                defaultValue={d.Slug}
                placeholder="team-handbook"
              />
            </Field>
            <div className={toolbar}>
              <ActionButton
                type="submit"
                label="Create site"
                icon={FolderPlus}
              />
              <ButtonLink href="/sites" label="Cancel" icon={X} />
            </div>
          </form>
        ) : (
          <p>
            Your account has no group available for new sites. Ask a workspace
            administrator to add you to a group that can create sites.
          </p>
        )}
      </PanelSection>
    </>
  );
}

export function DraftConflict({ d }: { d: SiteData }) {
  return (
    <>
      <Head tag="Draft" title="Review the current draft" />
      <PanelSection>
        <Notice text="The draft changed after you opened this form." error />
        {d.Action === "edit" ? (
          <>
            <Field
              id="unsaved"
              label={
                <>
                  Your unsaved text for <code>{d.Path}</code>
                </>
              }
            >
              <Textarea
                id="unsaved"
                className="min-h-80 font-mono"
                rows={20}
                readOnly
                defaultValue={d.Source}
              />
            </Field>
            <p>
              Keep this tab open to copy your text. Compare it with the current
              draft before saving again.
            </p>
          </>
        ) : d.Action === "delete" ? (
          <p>
            Review the current files, then select the files you still want to
            remove.
          </p>
        ) : (
          <p>
            Review the current files, then select your{" "}
            {d.Action === "import" ? "ZIP archive" : "file"} again.
          </p>
        )}
        <a
          className={linkStyle}
          href={navigationTarget(d.Base)}
          target="_blank"
          rel="noopener"
        >
          Open current draft in a new tab
        </a>
      </PanelSection>
    </>
  );
}

export function FileEditor({ d }: { d: SiteData }) {
  return (
    <>
      <Back d={d} />
      <Head
        tag={(d.S?.Name || "") + " / Draft"}
        title={d.Existing ? "Edit file" : "New file"}
      />
      <PanelSection>
        <Notice text={d.Error} error />
        {d.Error && (
          <p>
            Copy any changes you want to keep before{" "}
            <a className={linkStyle} href={navigationTarget(d.Base)}>
              opening the current draft
            </a>
            .
          </p>
        )}
        <form
          className="grid gap-5"
          method="post"
          action={postTarget(d.Base + "/edit")}
        >
          <Write d={d} />
          <Field
            id="file-path"
            label="File path"
            hint={
              <>
                For example, <code>index.html</code> or{" "}
                <code>styles/main.css</code>.
              </>
            }
          >
            <Input
              id="file-path"
              name="path"
              required
              maxLength={200}
              spellCheck={false}
              autoCapitalize="none"
              defaultValue={d.Path}
              readOnly={d.Existing}
              aria-describedby="file-path-hint"
            />
          </Field>
          {d.Existing && <Hidden name="existing" value="yes" />}
          <Field
            id="source"
            label="File contents"
            hint="HTML, CSS or JavaScript, up to 256 KiB. Saving changes the draft only."
          >
            <Textarea
              className="min-h-80 font-mono"
              id="source"
              name="source"
              rows={20}
              spellCheck={false}
              autoCapitalize="none"
              aria-describedby="source-hint"
              defaultValue={d.Source}
            />
          </Field>
          <div className={toolbar}>
            <ActionButton type="submit" label="Save file" icon={Save} />
            <ButtonLink href={d.Base} label="Cancel" icon={X} />
          </div>
        </form>
      </PanelSection>
    </>
  );
}

export function UploadSiteFile({ d }: { d: SiteData }) {
  const zip = d.Action === "import";
  return (
    <>
      <Back d={d} />
      <Head
        tag={(d.S?.Name || "") + " / Draft"}
        title={zip ? "Import ZIP" : "Upload file"}
      />
      <PanelSection>
        <Notice text={d.Error} error />
        <form
          className="grid gap-5"
          method="post"
          encType="multipart/form-data"
          action={postTarget(d.Base + "/" + (zip ? "import" : "upload"))}
        >
          <Write d={d} />
          <Field
            id="asset"
            label={zip ? "ZIP archive" : "Choose a file"}
            hint={
              zip
                ? "Up to 100 site files and 8 MiB unpacked. Include HTML, CSS, JavaScript, PNG, JPEG or GIF files. A single enclosing folder with an index.html is opened automatically."
                : "HTML, CSS and JavaScript: up to 256 KiB. PNG, JPEG and GIF: up to 2 MiB."
            }
          >
            <Input
              id="asset"
              type="file"
              name="file"
              accept={
                zip
                  ? ".zip,application/zip"
                  : ".html,.css,.js,.png,.jpg,.jpeg,.gif"
              }
              required
              aria-describedby="asset-hint"
            />
          </Field>
          {zip ? (
            <p>
              Files with the same paths will be replaced in the draft. Other
              files and the live site stay unchanged.
            </p>
          ) : (
            <Field
              id="upload-path"
              label={
                <>
                  Destination path{" "}
                  <span className="text-muted-foreground">(optional)</span>
                </>
              }
              hint="Leave blank to use the filename. Uploading replaces any draft file at that path."
            >
              <Input
                id="upload-path"
                name="path"
                maxLength={200}
                spellCheck={false}
                autoCapitalize="none"
                defaultValue={d.Path}
                aria-describedby="upload-path-hint"
                placeholder="images/team.png"
              />
            </Field>
          )}
          <div className={toolbar}>
            <ActionButton
              type="submit"
              label={zip ? "Import files" : "Upload to draft"}
              icon={FileUp}
            />
            <ButtonLink href={d.Base} label="Cancel" icon={X} />
          </div>
        </form>
      </PanelSection>
    </>
  );
}

export function ConfirmDelete({ d, sites }: { d: SiteData; sites: boolean }) {
  const selected = d.Selected || [],
    labels = sites ? d.Labels || [] : d.Paths || [];
  const noun = sites ? "site" : "file",
    plural = noun + (labels.length === 1 ? "" : "s"),
    title = (sites ? "Delete " : "Remove ") + plural;
  return (
    <>
      <Back d={sites ? undefined : d} />
      <Head tag={sites ? "Site directory" : "Draft files"} title={title} />
      <PanelSection>
        <Notice text={d.Error} error />
        <p>
          {sites
            ? "Deleting a site takes it offline and permanently removes its files, previews, saved revisions and releases. Its address stays reserved. This cannot be undone."
            : `Remove the following ${plural} from the draft? Published releases will keep their copies.`}
        </p>
        <ul className="list-disc space-y-1 pl-5">
          {labels.map((s, i) => (
            <li className="break-words" key={i}>
              {sites ? s : <code>{s}</code>}
            </li>
          ))}
        </ul>
        <form
          method="post"
          action={postTarget(sites ? "/sites" : d.Base + "/delete")}
        >
          {sites ? (
            <>
              <Token d={d} />
              <Hidden name="action" value="delete-sites" />
              {selected.map((s) => (
                <Hidden
                  key={s.DeletionValue}
                  name="site"
                  value={s.DeletionValue}
                />
              ))}
            </>
          ) : (
            <>
              <Write d={d} />
              {labels.map((p, i) => (
                <Hidden key={i} name="path" value={p} />
              ))}
            </>
          )}
          <Hidden name="proof" value={d.ReviewProof} />
          <div className={toolbar}>
            <ActionButton
              type="submit"
              variant="destructive"
              name="confirm"
              value={sites ? "delete" : "remove"}
              label={title}
              icon={Trash2}
            />
            <ButtonLink
              href={sites ? "/sites" : d.Base}
              label="Cancel"
              icon={X}
            />
          </div>
        </form>
      </PanelSection>
    </>
  );
}

export function RestoreRelease({ d }: { d: SiteData }) {
  return (
    <>
      <Back d={d} />
      <Head tag="Releases" title="Restore release" />
      <PanelSection>
        <p>
          The live address will show revision{" "}
          <code className="break-all">{d.Rev}</code>. Your current draft and
          audience stay unchanged.
        </p>
        <a
          className={linkStyle}
          href={navigationTarget(d.RestorePreview, true)}
        >
          Preview this revision
        </a>
        <form method="post" action={postTarget(d.Base + "/rollback")}>
          <Token d={d} />
          <Hidden name="request_id" value={d.Request} />
          <Hidden name="release" value={d.RestoreID} />
          <Hidden name="current" value={d.S?.ReleaseID || ""} />
          <div className={toolbar}>
            <ActionButton
              type="submit"
              label="Restore release"
              icon={History}
            />
            <ButtonLink href={d.Base} label="Cancel" icon={X} />
          </div>
        </form>
      </PanelSection>
    </>
  );
}
