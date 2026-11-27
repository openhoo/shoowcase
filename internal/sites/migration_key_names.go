package sites

import (
	"context"
	"database/sql"
)

func migrateAgentKeyNames(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "ALTER TABLE sc_agent_keys ADD COLUMN name_key TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,workspace,subject,name FROM sc_agent_keys ORDER BY created_at,id")
	if err != nil {
		return err
	}
	type owner struct{ workspace, subject string }
	type key struct {
		id, name string
		owner    owner
	}
	var keys []key
	// Reserve all existing names first so a generated suffix never changes a
	// distinct existing name. The oldest key in each duplicate set keeps its label.
	used := make(map[owner]map[string]string)
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.owner.workspace, &k.owner.subject, &k.name); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
		names := used[k.owner]
		if names == nil {
			names = make(map[string]string)
			used[k.owner] = names
		}
		if index := keyNameIndex(k.name); names[index] == "" {
			names[index] = k.id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		name, index := k.name, keyNameIndex(k.name)
		names := used[k.owner]
		if names[index] != k.id {
			for n := 2; ; n++ {
				name = numberedKeyName(k.name, n)
				index = keyNameIndex(name)
				if names[index] == "" {
					names[index] = k.id
					break
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sc_agent_keys SET name=$1,name_key=$2 WHERE id=$3", name, index, k.id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "CREATE UNIQUE INDEX sc_agent_keys_owner_name ON sc_agent_keys(workspace,subject,name_key)")
	return err
}
