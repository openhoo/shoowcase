import type { SiteData } from "./sites-model";
import { SiteDirectory } from "./sites-directory";
import { SiteDetail } from "./sites-detail";
import {
  ConfirmDelete,
  CreateSite,
  FileEditor,
  RestoreRelease,
  UploadSiteFile,
} from "./sites-forms";

const views = {
  list: SiteDirectory,
  detail: SiteDetail,
  "new-site": CreateSite,
  edit: FileEditor,
  upload: UploadSiteFile,
  restore: RestoreRelease,
  delete: ({ d }: { d: SiteData }) => <ConfirmDelete d={d} sites={false} />,
  "delete-sites": ({ d }: { d: SiteData }) => <ConfirmDelete d={d} sites />,
} as const;

export function Sites({ page }: { page: { view: string; data: SiteData } }) {
  const Screen = views[page.view as keyof typeof views];
  if (!Screen) throw new Error("Unsupported Sites view");
  return (
    <div className="min-w-0 space-y-6">
      <Screen d={page.data} />
    </div>
  );
}
