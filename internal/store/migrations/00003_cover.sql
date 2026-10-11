-- +goose Up
ALTER TABLE posts ADD COLUMN cover_path TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN cover_custom INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE posts DROP COLUMN cover_custom;
ALTER TABLE posts DROP COLUMN cover_path;
