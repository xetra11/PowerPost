-- +goose Up
-- Hand-made framing per platform: {"tiktok": {"scale": 1.2, "x": 0.5, "y": 0.4}, ...}
ALTER TABLE assets ADD COLUMN framing TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE assets DROP COLUMN framing;
