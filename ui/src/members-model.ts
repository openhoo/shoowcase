import type { SiteListing } from "./sites-model";

export interface Account {
  Subject: string;
  DisplayName: string;
  Role: string;
  IdentityRole: string;
  Ceiling: string;
  Disabled: boolean;
  CreatedAt: string;
}
export interface Group {
  ID: string;
  Name: string;
  Role: string;
  Members: number;
  Version: number;
}
export interface Directory {
  Query: string;
  Role: string;
  State: string;
  Sort: string;
  Filtered: boolean;
  Total: number;
  Matches: number;
  Prev: string;
  Next: string;
}
export interface AgentKey {
  ID: string;
  Name: string;
  Subject: string;
  Scope: string;
  CreatedAt: string;
  ExpiresAt: string;
  State: string;
}
export interface AuditEvent {
  sequence: number;
  at: string;
  subject: string;
  action: string;
  target?: string;
  previous?: string;
  value?: string;
}
export interface Resource {
  Sites: number;
  Bytes: number;
}
export interface Budget {
  ID: string;
  Name: string;
  Version: number;
  Usage: Resource;
  Effective: Resource;
  Limits: Resource;
  Workspace: boolean;
  CSRF: string;
  SiteLimit: string;
  StorageLimit: string;
  Unit: string;
}
export interface SiteQuota {
  ID: string;
  Name: string;
  GroupID: string;
  GroupName: string;
  Href: string;
  Bytes: number;
  Version: number;
}
export interface MemberData {
  LocalAdmin: boolean;
  LocalUser: boolean;
  OIDCAccountURL: string;
  CSRF: string;
  Workspace: string;
  Subject: string;
  Name: string;
  Role: string;
  Endpoint: string;
  Enrolled: boolean;
  NameValue: string;
  Scope: string;
  Days: string;
  Secret: string;
  Err: string;
  NameError: string;
  Notice: string;
  Creation: string;
  Scopes: string[];
  Keys: AgentKey[] | null;
  Member:
    (Account & { Manage: boolean; Local: boolean; Ceilings: string[] }) | null;
  Members: Account[] | null;
  Directory: Directory;
  SiteListing: SiteListing | null;
  EventNames: Record<string, string>;
  Events: AuditEvent[] | null;
  Settings: { ID: string; DisplayName: string; Mode: string; Version: number };
  Groups: Group[] | null;
  Group: Group | null;
  GroupMembers: string[] | null;
  Quota: {
    HasGroupLimits: boolean;
    HasAssignments: boolean;
    Workspace: Budget;
    Groups: Budget[] | null;
    EditBudget: Budget | null;
    Unassigned: Resource;
    Sites: SiteQuota[] | null;
    Selected: SiteQuota | null;
    MaxSites: number;
    MaxBytes: number;
  };
  Stats: { Members: number; Sites: number; ActiveKeys: number };
  Next: string;
  View: string;
  Admin: boolean;
}
export interface QuickData extends MemberData {
  Client: string;
  Platform: string;
  ClientName: string;
  Command: string;
  Prompt: string;
  Clients: { ID: string; Name: string }[];
  CompanyProxy: boolean;
  UninstallUnix: string;
  UninstallWindows: string;
}
export const roles: Record<string, string> = {
  reader: "Viewer",
  editor: "Editor",
  publisher: "Publisher",
  admin: "Admin",
  "": "Use sign-in role",
};
export const role = (v: string) => roles[v] ?? "Unknown role";
export const plural = (n: number, noun: string) =>
  n === 1 ? noun : noun + "s";
export const urlValue = encodeURIComponent;
export function longDate(value: string) {
  const t = new Date(value);
  const date = t.toLocaleDateString("en-US", {
    timeZone: "UTC",
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  return date + " at " + t.toISOString().slice(11, 16) + " UTC";
}
