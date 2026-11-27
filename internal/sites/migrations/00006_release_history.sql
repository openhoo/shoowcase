-- +goose Up
-- get_site pages a site's history by timestamp, then release ID for ties.
CREATE INDEX sc_releases_history ON sc_releases(site, created_at DESC, id DESC);
