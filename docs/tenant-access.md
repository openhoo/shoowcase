# Tenant access

A workspace is one tenant. Its ID comes from the signed workspace claim; a form, site ID or group ID cannot select another workspace. The database is bound to one identity issuer. The same subject in two workspaces has separate memberships, keys and group assignments. Changing a workspace’s display name leaves its identity and URLs unchanged.

## Choose how access is granted

Open **Workspace admin → Workspace settings** as an enrolled identity-provider administrator.

| Access mode | Who can access the workspace? | Role |
| --- | --- | --- |
| Identity (default) | People with a valid workspace claim. MCP and private delivery permit unenrolled identities. The console and personal keys require a local account. | Signed identity-provider role, capped by any individual role limit. Groups may be prepared but do not affect access. |
| Managed | Active local members. An administrator provisions each person by their exact OIDC subject. Only a user with a verified administrator role may self-enroll. | Members start as readers. The strongest group role applies, capped by the current identity-provider role and any individual limit. An enrolled identity-provider administrator retains admin access. |

In either mode a suspended account is denied private access. An intentionally public live release remains accessible anonymously, subject to the instance-wide public sharing setting.

![Workspace settings with Managed members and groups selected.](media/admin/access-mode.png)

## Set up a managed tenant

1. Sign in with a workspace administrator account and complete enrollment. The identity provider owns the workspace ID and administrator role.
2. Under **Members**, provision the exact OIDC subject and a display name. Choose the stable subject issued to Shoowcase, not a login alias or email address unless it actually is the subject. Provisioning alone does not create an identity or credentials. A signed-in person who needs provisioning sees **Workspace access required** and their exact sign-in ID on the console. After you add them, they can reload the original page.
3. Under **Groups**, create groups, assign each a role, then add members. An Authors group might have editor rights; a Release team might have publisher rights. A member of both can publish only if the identity provider also permits publication.
4. Review all existing members, groups, keys and the current admin before switching to managed mode. Existing accounts stay enrolled. An existing publisher without a group becomes a reader immediately. A user who previously only used an IdP access token needs to be provisioned.
5. Save the managed setting, then verify one reader, one editor and one publisher against a private test site. The **Members** view shows effective roles. Switch back to identity mode only if returning access to all signed workspace identities is intended.

![The Releases group grants Publisher access and has one member. The selected group lets an administrator add and remove people by sign-in ID.](media/admin/groups.png)

| Role | Permissions |
| --- | --- |
| Reader | List sites, inspect revisions and read private previews and workspace releases. |
| Editor | Reader permissions plus creating sites and changing drafts. |
| Publisher | Editor permissions plus publishing, rolling back and changing visibility. Public changes still require browser approval. |
| Administrator | Publisher permissions plus member, group, workspace and audit administration. This role cannot be granted by a group or personal key. |

Group roles apply across the workspace. There are no per-site ACLs or nested groups. Each workspace supports 2,000 member records and 128 groups. An account’s IdP role follows its most recently verified assertion; live IdP directory changes are observed on sign-in or token use. For immediate local offboarding, suspend the member. This also revokes their personal keys. Restoring an account does not reactivate those keys.

After a group removal, role change or switch to managed mode, Shoowcase rechecks existing key and signed action permissions at use. Admin mutations check current membership under the same database lock as the write. If two admins edit the same group or settings, the stale form reports a conflict; reload before retrying.

## Database upgrade

Stop and drain older writers before starting this version on a backup or an existing database. Schema version 3 preserves sites, account limits, suspension and key records. It gives existing tenants identity mode and empty group lists, so upgrading does not change their access. A switch to managed mode is explicit per tenant. Keep a restorable pre-upgrade backup: older binaries cannot use a version 3 database.
