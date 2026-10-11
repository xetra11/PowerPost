-- +goose Up
CREATE TABLE posts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    title           TEXT    NOT NULL DEFAULT '',
    kind            TEXT    NOT NULL DEFAULT 'video',
    caption         TEXT    NOT NULL DEFAULT '',
    hashtags        TEXT    NOT NULL DEFAULT '[]',   -- JSON array, without '#'
    aspect_strategy TEXT    NOT NULL DEFAULT 'blur', -- blur | crop | pad
    cover_ms        INTEGER NOT NULL DEFAULT 0,
    status          TEXT    NOT NULL DEFAULT 'draft', -- draft | approved | publishing | published | partial | failed
    approved_hash   TEXT    NOT NULL DEFAULT '',
    created_by      TEXT    NOT NULL DEFAULT 'ui',   -- ui | mcp
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE TABLE assets (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id       INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    path          TEXT    NOT NULL,
    original_name TEXT    NOT NULL DEFAULT '',
    thumb_path    TEXT    NOT NULL DEFAULT '',
    info          TEXT    NOT NULL DEFAULT '{}',     -- media.Info JSON
    created_at    TEXT    NOT NULL
);
CREATE INDEX assets_post ON assets(post_id);

CREATE TABLE targets (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id           INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    platform          TEXT    NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    caption_override  TEXT,                          -- NULL = use post caption
    hashtags_override TEXT,                          -- NULL = use post hashtags; JSON array
    options           TEXT    NOT NULL DEFAULT '{}',
    status            TEXT    NOT NULL DEFAULT 'idle', -- idle | queued | publishing | published | failed
    stage             TEXT    NOT NULL DEFAULT '',
    remote_id         TEXT    NOT NULL DEFAULT '',
    permalink         TEXT    NOT NULL DEFAULT '',
    note              TEXT    NOT NULL DEFAULT '',
    error             TEXT    NOT NULL DEFAULT '',
    published_at      TEXT,
    UNIQUE (post_id, platform)
);

CREATE TABLE renditions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id      INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    platform      TEXT    NOT NULL,
    profile_hash  TEXT    NOT NULL,
    plan          TEXT    NOT NULL DEFAULT '{}',     -- media.Plan JSON
    path          TEXT    NOT NULL,                  -- shared by renditions with the same asset + hash
    info          TEXT,                              -- media.Info JSON once ready
    status        TEXT    NOT NULL DEFAULT 'pending', -- pending | processing | ready | failed
    progress      REAL    NOT NULL DEFAULT 0,
    checksum      TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    UNIQUE (asset_id, platform)
);
CREATE INDEX renditions_path ON renditions(path);

CREATE TABLE publish_attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    target_id   INTEGER NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    status      TEXT    NOT NULL DEFAULT 'running',  -- running | succeeded | failed
    error       TEXT    NOT NULL DEFAULT '',
    log         TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE connections (
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

CREATE TABLE oauth_states (
    state      TEXT PRIMARY KEY,
    platform   TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE jobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT    NOT NULL,                     -- transcode | publish
    payload    TEXT    NOT NULL DEFAULT '{}',
    status     TEXT    NOT NULL DEFAULT 'queued',    -- queued | running | done | failed
    error      TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);
CREATE INDEX jobs_queue ON jobs(status, type, id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE jobs;
DROP TABLE oauth_states;
DROP TABLE connections;
DROP TABLE publish_attempts;
DROP TABLE renditions;
DROP TABLE targets;
DROP TABLE assets;
DROP TABLE posts;
