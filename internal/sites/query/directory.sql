-- name: Directory :many
SELECT s.id, s.name, s.visibility, s.draft, s.live, s.release_id,
       CAST(coalesce(sg.group_id, '') AS text) AS group_id,
       (SELECT count(*) FROM sc_files f WHERE f.revision = s.draft) AS file_count,
       CAST(coalesce((SELECT max(a.created_at) FROM sc_audit a
                      WHERE a.workspace = s.workspace AND a.site = s.id),
                     r.created_at, 0) AS bigint) AS updated_ms
FROM sc_sites s
LEFT JOIN sc_revisions r ON r.id = s.draft AND r.site = s.id
LEFT JOIN sc_site_groups sg ON sg.workspace = s.workspace AND sg.site = s.id
WHERE s.workspace = sqlc.arg(workspace) AND s.id > sqlc.arg(after_id)
  AND (CAST(sqlc.arg(all_sites) AS boolean)
       OR (SELECT creator.subject FROM sc_audit creator
           WHERE creator.workspace = s.workspace AND creator.site = s.id AND creator.action = 'create_site'
           ORDER BY creator.created_at LIMIT 1) = sqlc.arg(subject)
       OR EXISTS (SELECT 1 FROM sc_group_members gm
                  WHERE gm.workspace = s.workspace AND gm.group_id = sg.group_id
                    AND gm.subject = sqlc.arg(subject)))
ORDER BY s.id
LIMIT sqlc.arg(max_sites);
