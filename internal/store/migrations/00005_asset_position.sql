-- +goose Up
-- Image posts have several assets (a carousel); position is their order.
ALTER TABLE assets ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE assets DROP COLUMN position;
