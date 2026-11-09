-- League IDs each bot holds, per source, as of the last refresh.
CREATE TABLE bot_leagues (
  bot          TEXT    NOT NULL,
  league_id    INTEGER NOT NULL,
  source       TEXT    NOT NULL,
  refreshed_at TEXT    NOT NULL,
  PRIMARY KEY (bot, league_id, source)
);

-- The outcome of each bot's last league refresh. ok_at is the last success,
-- so a bot that administers nothing still has a row.
CREATE TABLE bot_league_refresh (
  bot      TEXT PRIMARY KEY,
  ok_at    TEXT,
  error    TEXT,
  error_at TEXT
);
