-- name: Directory :many
SELECT s.id, s.name, s.visibility, s.draft, s.live,
       (SELECT count(*) FROM sc_files f WHERE f.revision = s.draft) AS file_count,
       CAST(coalesce((SELECT max(a.created_at) FROM sc_audit a
                      WHERE a.workspace = s.workspace AND a.site = s.id),
                     r.created_at, 0) AS bigint) AS updated_ms
FROM sc_sites s
LEFT JOIN sc_revisions r ON r.id = s.draft AND r.site = s.id
WHERE s.workspace = sqlc.arg(workspace)
ORDER BY s.id
LIMIT sqlc.arg(max_sites);
