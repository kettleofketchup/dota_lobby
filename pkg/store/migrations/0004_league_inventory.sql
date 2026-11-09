-- When each league first appeared in a bot's list. Kept across refreshes
-- while the league stays; rows from before this migration start at their
-- last refresh, the earliest time known for them.
ALTER TABLE bot_leagues ADD COLUMN first_seen_at TEXT;
UPDATE bot_leagues SET first_seen_at = refreshed_at;

-- Every league that appeared in or disappeared from a bot's list, so an
-- admin can see when a ticket was granted or taken away. The first list a
-- bot ever reports counts as added.
CREATE TABLE bot_league_changes (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  bot       TEXT    NOT NULL,
  league_id INTEGER NOT NULL,
  source    TEXT    NOT NULL,
  -- added | removed
  change    TEXT    NOT NULL,
  at        TEXT    NOT NULL
);
CREATE INDEX bot_league_changes_bot ON bot_league_changes(bot, id);
