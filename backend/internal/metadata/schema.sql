CREATE TABLE users (
 id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL,
 admin INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 1,
 home_id TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE nodes (
 id TEXT PRIMARY KEY, parent_id TEXT REFERENCES nodes(id), owner_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('folder','file')),
 size INTEGER NOT NULL DEFAULT 0 CHECK(size >= 0), created_at INTEGER NOT NULL,
 deleted_at INTEGER, trash_root TEXT
);
CREATE UNIQUE INDEX node_names ON nodes(parent_id,name) WHERE deleted_at IS NULL;
CREATE INDEX node_children ON nodes(parent_id,deleted_at);
CREATE INDEX node_trash ON nodes(trash_root);
CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE memberships (
 group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, PRIMARY KEY(group_id,user_id)
);
CREATE TABLE grants (
 node_id TEXT NOT NULL REFERENCES nodes(id), principal_type TEXT NOT NULL CHECK(principal_type IN ('user','group')),
 principal_id TEXT NOT NULL, role INTEGER NOT NULL CHECK(role BETWEEN 1 AND 3),
 PRIMARY KEY(node_id,principal_type,principal_id)
);
CREATE TABLE sessions (
 hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), csrf TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE TABLE devices (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL,
 hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE device_requests (
 id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, name TEXT NOT NULL,
 user_id TEXT REFERENCES users(id), expires_at INTEGER NOT NULL, consumed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE uploads (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), folder_id TEXT NOT NULL REFERENCES nodes(id),
 name TEXT NOT NULL, length INTEGER NOT NULL CHECK(length >= 0), offset INTEGER NOT NULL DEFAULT 0 CHECK(offset >= 0),
 state TEXT NOT NULL CHECK(state IN ('open','committing','complete')), file_id TEXT NOT NULL UNIQUE,
 replace_id TEXT REFERENCES nodes(id), updated_at INTEGER NOT NULL,
 request_key TEXT, UNIQUE(user_id,request_key)
);
CREATE TABLE shares (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), user_id TEXT NOT NULL REFERENCES users(id),
 hash TEXT NOT NULL UNIQUE, password TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
 expires_at INTEGER, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX share_creator ON shares(user_id,revoked);
CREATE INDEX share_node ON shares(node_id,revoked);
CREATE TABLE share_sessions (
 hash TEXT PRIMARY KEY, share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
 expires_at INTEGER NOT NULL
);
CREATE TABLE purges (root_id TEXT PRIMARY KEY REFERENCES nodes(id));
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
