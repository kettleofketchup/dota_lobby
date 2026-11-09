-- The lobby as it was when each event happened (no password), so an event
-- reads the same in a webhook and in the events endpoint however the lobby
-- changed afterwards. Events stored before this migration have none.
ALTER TABLE lobby_events ADD COLUMN lobby TEXT;

-- One row per event and webhook URL, written in the same transaction as
-- the event. Rows stay until the retention sweep deletes their lobby.
CREATE TABLE webhook_deliveries (
  event_id     TEXT    NOT NULL REFERENCES lobby_events(id),
  url          TEXT    NOT NULL,
  lobby_id     TEXT    NOT NULL,
  seq          INTEGER NOT NULL,
  -- pending | delivered | dead
  status       TEXT    NOT NULL,
  attempts     INTEGER NOT NULL DEFAULT 0,
  next_at      TEXT    NOT NULL,
  last_status  INTEGER,
  last_error   TEXT,
  delivered_at TEXT,
  PRIMARY KEY (event_id, url)
);
CREATE INDEX webhook_due ON webhook_deliveries(status, next_at);
-- Finds the lowest pending seq per (url, lobby): deliveries go in order.
CREATE INDEX webhook_order ON webhook_deliveries(url, lobby_id, status, seq);
