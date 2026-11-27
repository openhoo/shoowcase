-- +goose Up
-- An unenrolled OIDC identity would otherwise regain access in identity mode.
-- Store only a workspace-specific digest; keep no subject, profile or credentials.
CREATE TABLE sc_deleted_identities (
    workspace TEXT NOT NULL REFERENCES sc_workspace(id),
    identity_digest TEXT NOT NULL,
    blocked BOOLEAN NOT NULL,
    cutoff BIGINT NOT NULL,
    PRIMARY KEY (workspace, identity_digest)
);
CREATE INDEX sc_audit_creator ON sc_audit(workspace, subject, site) WHERE action='create_site';
