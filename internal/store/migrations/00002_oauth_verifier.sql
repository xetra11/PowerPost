-- +goose Up
ALTER TABLE oauth_states ADD COLUMN verifier TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE oauth_states DROP COLUMN verifier;
