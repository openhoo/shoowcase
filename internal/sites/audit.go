package sites

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Backfill the history the older release still holds. A workspace lock orders
// subsequent writes, including across replicas. Pollers can use sequence as a
// durable checkpoint without relying on wall-clock order or global tenant IDs.
var auditSchema = []string{
	`CREATE TABLE sc_workspace_audit (
	    workspace TEXT NOT NULL REFERENCES sc_workspace(id), seq BIGINT NOT NULL, at BIGINT NOT NULL,
	    subject TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL,
	    previous_value TEXT NOT NULL DEFAULT '', new_value TEXT NOT NULL DEFAULT '',
	    credential_id TEXT NOT NULL DEFAULT '', draft_revision TEXT NOT NULL DEFAULT '', live_revision TEXT NOT NULL DEFAULT '', release_id TEXT NOT NULL DEFAULT '',
	    PRIMARY KEY(workspace,seq))`,
	`INSERT INTO sc_workspace_audit(workspace,seq,at,subject,action,target,new_value,draft_revision,live_revision,release_id)
	    SELECT workspace,ROW_NUMBER() OVER (PARTITION BY workspace ORDER BY at,source,id),at,subject,action,target,value,draft_revision,live_revision,release_id
	    FROM (SELECT workspace,at,0 AS source,id,subject,action,target,'' AS value,'' AS draft_revision,'' AS live_revision,'' AS release_id FROM sc_account_events
	          UNION ALL
	          SELECT workspace,created_at*1000 AS at,1 AS source,id,subject,action,site,visibility,revision,live_revision,release_id FROM sc_audit) AS older`,
}

type AuditEvent struct {
	Sequence      int64     `json:"sequence"`
	Workspace     string    `json:"workspace"`
	At            time.Time `json:"at"`
	Subject       string    `json:"subject"`
	Action        string    `json:"action"`
	Target        string    `json:"target,omitempty"`
	Previous      string    `json:"previous,omitempty"`
	Value         string    `json:"value,omitempty"`
	CredentialID  string    `json:"credential_id,omitempty"`
	DraftRevision string    `json:"draft_revision,omitempty"`
	LiveRevision  string    `json:"live_revision,omitempty"`
	ReleaseID     string    `json:"release_id,omitempty"`
}

// Call only under lockWorkspace in the transaction that performs the change.
// Do not pass raw requests, errors, credentials, or user content as details.
func appendAudit(ctx context.Context, tx *sql.Tx, a Actor, event AuditEvent) error {
	key, _ := ctx.Value(keyCredentialID{}).(string)
	_, err := tx.ExecContext(ctx, `INSERT INTO sc_workspace_audit(workspace,seq,at,subject,action,target,previous_value,new_value,credential_id,draft_revision,live_revision,release_id)
		VALUES ($1,(SELECT COALESCE(MAX(seq),0)+1 FROM sc_workspace_audit WHERE workspace=$1),$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		a.Workspace, time.Now().UnixMicro(), a.Subject, event.Action, event.Target, event.Previous, event.Value, key, event.DraftRevision, event.LiveRevision, event.ReleaseID)
	return err
}

// Only verified identity-provider/browser transitions enter the tenant ledger.
// There is no trustworthy tenant to attribute a malformed callback to.
func (s *Store) RecordSession(ctx context.Context, a Actor, action string) error {
	if action != "auth.login" && action != "auth.logout" {
		return bad("Unknown session event.")
	}
	tx, err := s.accountWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkSessionContext(ctx, tx, a); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, a, AuditEvent{Action: action}); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordDenied accepts only stable operation names, never raw form actions,
// paths, tool arguments or credential values. The caller has verified the
// identity, but it need not be entitled to the requested operation.
func (s *Store) RecordDenied(ctx context.Context, a Actor, operation string) error {
	switch operation {
	case "admin", "audit", "prepare_upload", "list_site_groups", "list_sites", "get_site", "create_site", "upload_files", "publish_site", "rollback_site", "set_site_access", "approve_public_change":
	default:
		return bad("Unknown authorization event.")
	}
	if err := actorErr(a, 1); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, s.driver, a.Workspace); err != nil {
		return err
	}
	// A late denied request must not put an erased subject back in the ledger.
	if denied, err := identityBlocked(ctx, tx, a); err != nil || denied {
		return err
	}
	if key, ok := ctx.Value(keyCredentialID{}).(string); ok {
		if _, err := resolveKeyID(ctx, tx, a, key); err != nil {
			var invalid *Error
			if errors.As(err, &invalid) {
				return nil
			}
			return err
		}
	}
	if err = appendAudit(ctx, tx, a, AuditEvent{Action: "access.denied", Target: operation}); err != nil {
		return err
	}
	return tx.Commit()
}

// AuditPage returns newest-first for the console or ascending sequence after a
// collector checkpoint. Both authorization and rows use the same DB snapshot.
func (s *Store) AuditPage(ctx context.Context, a Actor, cursor int64, ascending bool, limit int) (events []AuditEvent, more bool, err error) {
	if cursor < 0 || limit < 1 || limit > 200 {
		return nil, false, bad("Invalid audit page.")
	}
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if _, err = authorizeMember(ctx, tx, a, 4); err != nil {
		return nil, false, err
	}
	query := "SELECT seq,at,subject,action,target,previous_value,new_value,credential_id,draft_revision,live_revision,release_id FROM sc_workspace_audit WHERE workspace=$1"
	args := []any{a.Workspace}
	switch {
	case ascending:
		query += " AND seq>$2 ORDER BY seq LIMIT $3"
		args = append(args, cursor, limit+1)
	case cursor > 0:
		query += " AND seq<$2 ORDER BY seq DESC LIMIT $3"
		args = append(args, cursor, limit+1)
	default:
		query += " ORDER BY seq DESC LIMIT $2"
		args = append(args, limit+1)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	events = make([]AuditEvent, 0, limit)
	for rows.Next() {
		if len(events) == limit {
			return events, true, nil
		}
		v := AuditEvent{Workspace: a.Workspace}
		var micros int64
		if err = rows.Scan(&v.Sequence, &micros, &v.Subject, &v.Action, &v.Target, &v.Previous, &v.Value, &v.CredentialID, &v.DraftRevision, &v.LiveRevision, &v.ReleaseID); err != nil {
			return nil, false, err
		}
		v.At = time.UnixMicro(micros).UTC()
		events = append(events, v)
	}
	return events, false, rows.Err()
}
