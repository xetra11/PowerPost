-- +goose Up
-- Projects (one per game/brand) with their own settings and account connections.
CREATE TABLE projects (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT    NOT NULL UNIQUE,
    name       TEXT    NOT NULL,
    settings   TEXT    NOT NULL DEFAULT '{}',  -- brand, default platforms, default aspect (JSON)
    created_at TEXT    NOT NULL
);

-- Existing data becomes the first project, named after the brand's game.
INSERT INTO projects (id, slug, name, settings, created_at)
SELECT 1,
       lower(replace(trim(n), ' ', '-')),
       n,
       COALESCE((SELECT value FROM settings WHERE key = 'settings'), '{}'),
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM (SELECT COALESCE((SELECT NULLIF(trim(json_extract(value, '$.brand.gameName')), '') FROM settings WHERE key = 'settings'), 'My project') AS n);

-- SQLite can't add a REFERENCES column with a non-NULL default; the app keeps it consistent.
ALTER TABLE posts ADD COLUMN project_id INTEGER NOT NULL DEFAULT 1;
CREATE INDEX posts_project ON posts(project_id);

ALTER TABLE oauth_states ADD COLUMN project_id INTEGER NOT NULL DEFAULT 1;

CREATE TABLE connections_new (
    project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    platform           TEXT NOT NULL,
    account_id         TEXT NOT NULL DEFAULT '',
    account_name       TEXT NOT NULL DEFAULT '',
    access_token_enc   BLOB,
    refresh_token_enc  BLOB,
    expires_at         TEXT,
    refresh_expires_at TEXT,
    scopes             TEXT NOT NULL DEFAULT '',
    updated_at         TEXT NOT NULL,
    PRIMARY KEY (project_id, platform)
);
INSERT INTO connections_new (project_id, platform, account_id, account_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, scopes, updated_at)
SELECT 1, platform, account_id, account_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, scopes, updated_at FROM connections;
DROP TABLE connections;
ALTER TABLE connections_new RENAME TO connections;

-- +goose Down
CREATE TABLE connections_old (
    platform           TEXT PRIMARY KEY,
    account_id         TEXT NOT NULL DEFAULT '',
    account_name       TEXT NOT NULL DEFAULT '',
    access_token_enc   BLOB,
    refresh_token_enc  BLOB,
    expires_at         TEXT,
    refresh_expires_at TEXT,
    scopes             TEXT NOT NULL DEFAULT '',
    updated_at         TEXT NOT NULL
);
INSERT INTO connections_old (platform, account_id, account_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, scopes, updated_at)
SELECT platform, account_id, account_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, scopes, updated_at FROM connections WHERE project_id = 1;
DROP TABLE connections;
ALTER TABLE connections_old RENAME TO connections;
ALTER TABLE oauth_states DROP COLUMN project_id;
DROP INDEX posts_project;
ALTER TABLE posts DROP COLUMN project_id;
DROP TABLE projects;
