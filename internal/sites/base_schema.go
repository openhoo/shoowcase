package sites

import (
	_ "embed"
	"strings"
)

// The frozen v0 baseline has one statement per line. Both the legacy startup
// migrations and sqlc read this file; later schema changes belong in migrations.
//
//go:embed schema/base.sql
var baseDDL string

var schema = strings.Split(strings.TrimSpace(baseDDL), "\n")

// The group tables are used by their original startup migrations and the
// directory query generator. Existing databases already contain both tables.
//
//go:embed schema/sc_group_members.sql
var groupMembersDDL string

//go:embed schema/sc_site_groups.sql
var siteGroupsDDL string
