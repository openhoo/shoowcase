CREATE TABLE sc_group_members (
    workspace TEXT NOT NULL, group_id TEXT NOT NULL, subject TEXT NOT NULL,
    PRIMARY KEY(workspace,group_id,subject),
    FOREIGN KEY(workspace,group_id) REFERENCES sc_groups(workspace,id) ON DELETE CASCADE,
    FOREIGN KEY(workspace,subject) REFERENCES sc_members(workspace,subject));
