// View data produced by Go's site console. Nested structs with JSON tags
// (group options, file metadata and releases) retain their wire field names.
export interface SiteEntry {
  ID: string;
  Name: string;
  Address: string;
  Href: string;
  State: string;
  StateLabel: string;
  AudienceLabel: string;
  DeletionValue: string;
  FileCount: number;
  Updated: string;
  UpdatedLabel: string;
}
interface SiteDirectory {
  Sites: SiteEntry[] | null;
  Filtered: boolean;
  Query: string;
  State: string;
  Audience: string;
  Sort: string;
  MatchCount: number;
  Total: number;
  Changes: number;
  Published: number;
  Drafts: number;
}
interface Details {
  Name: string;
  Visibility: string;
  ConfiguredVisibility: string;
  StorageRestriction: string;
  Revision: string;
  LiveRevision: string;
  ReleaseID: string;
  Files: { path: string; size: number }[] | null;
  Releases:
    { release_id: string; revision: string; created_at: string }[] | null;
  NextCursor: string;
}
export interface SiteListing {
  InAdmin: boolean;
  C: {
    WorkspaceLabel: string;
    Actor: { Role: string };
    Identity: { CSRF: string };
  };
  Directory: SiteDirectory;
}
export const siteListURL = (inAdmin: boolean) =>
  inAdmin ? "/admin?view=sites" : "/sites";
export interface SiteData extends SiteListing {
  DeletionValue: string;
  Selected: { DeletionValue: string }[] | null;
  Labels: string[] | null;
  Paths: string[] | null;
  Creation: {
    Groups: { id: string; name: string }[] | null;
    AllowUnassigned: boolean;
  };
  GroupID: string;
  S: Details | null;
  History: Details | null;
  SiteURL: string;
  PreviewURL: string;
  RestoreID: string;
  RestorePreview: string;
  Base: string;
  Name: string;
  Slug: string;
  Path: string;
  Source: string;
  Rev: string;
  Request: string;
  Action: string;
  Notice: string;
  Error: string;
  ReviewProof: string;
  CanEdit: boolean;
  CanPublish: boolean;
  AllowPublic: boolean;
  Existing: boolean;
  HasIndex: boolean;
}

export const shortID = (id: string) => id.slice(0, 8);
export const audienceName = (audience: string) =>
  audience === "public"
    ? "Public"
    : audience === "offline"
      ? "Offline"
      : "Workspace";
export const humanBytes = (n: number) =>
  n < 1024
    ? n + " B"
    : n < 1048576
      ? (n / 1024).toFixed(1) + " KiB"
      : (n / 1048576).toFixed(1) + " MiB";
export const utcTime = (iso: string) => {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return (
    date.toLocaleString("en-GB", {
      timeZone: "UTC",
      day: "2-digit",
      month: "short",
      year: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }) + " UTC"
  );
};
