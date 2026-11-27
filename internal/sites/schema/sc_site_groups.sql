CREATE TABLE sc_site_groups (
	workspace TEXT NOT NULL, site TEXT NOT NULL, group_id TEXT NOT NULL, PRIMARY KEY(workspace,site),
	FOREIGN KEY(workspace,site) REFERENCES sc_sites(workspace,id),
	FOREIGN KEY(workspace,group_id) REFERENCES sc_groups(workspace,id));
