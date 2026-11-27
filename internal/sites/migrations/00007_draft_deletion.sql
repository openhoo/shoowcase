-- +goose Up
-- Once allocated a site address must never pass to another tenant.
CREATE TABLE sc_reserved_sites (id TEXT PRIMARY KEY);
INSERT INTO sc_reserved_sites(id) SELECT id FROM sc_sites;
-- A digest is reclaimable only after the last manifest reference is gone.
CREATE INDEX sc_files_digest ON sc_files(digest);
