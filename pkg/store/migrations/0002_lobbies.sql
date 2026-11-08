-- One row per lobby the service manages, from the accepted request until
-- the retention sweep deletes it.
CREATE TABLE lobbies (
  id              TEXT    PRIMARY KEY,
  external_ref    TEXT    NOT NULL,
  state           TEXT    NOT NULL,
  state_reason    TEXT,
  state_since     TEXT    NOT NULL,
  bot             TEXT,
  gc_lobby_id     INTEGER,
  league_id       INTEGER NOT NULL DEFAULT 0,
  league_verified INTEGER,
  -- The create request without the roster and password, as PATCH merged it (JSON).
  spec            TEXT    NOT NULL,
  -- Plain text: the file is owner-only (0600) and the API hands it out.
  pass_key        TEXT,
  -- The full detail set last sent to the GC (JSON).
  details         TEXT,
  host_seat       TEXT,
  launch_attempts INTEGER NOT NULL DEFAULT 0,
  match_id        INTEGER,
  outcome         INTEGER,
  match           TEXT,
  seq             INTEGER NOT NULL DEFAULT 0,
  version         INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL,
  expires_at      TEXT,
  ended_at        TEXT
);

-- external_ref is the idempotency key among lobbies that have not ended.
CREATE UNIQUE INDEX lobbies_active_ref ON lobbies(external_ref)
  WHERE state NOT IN ('finished', 'failed', 'closed', 'timed_out');
CREATE INDEX lobbies_state ON lobbies(state);
CREATE INDEX lobbies_ref ON lobbies(external_ref);

-- The roster, with what has been done for each player.
CREATE TABLE lobby_players (
  lobby_id         TEXT    NOT NULL REFERENCES lobbies(id),
  account_id       INTEGER NOT NULL,
  side             TEXT    NOT NULL,
  position         INTEGER NOT NULL DEFAULT 0,
  team_ref         TEXT,
  captain          INTEGER NOT NULL DEFAULT 0,
  ready            INTEGER NOT NULL DEFAULT 0,
  invites_sent     INTEGER NOT NULL DEFAULT 0,
  last_invite_at   TEXT,
  invite_confirmed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (lobby_id, account_id)
);

-- Every event, numbered per lobby without gaps, written in the same
-- transaction as the change it reports.
CREATE TABLE lobby_events (
  id          TEXT    PRIMARY KEY,
  lobby_id    TEXT    NOT NULL REFERENCES lobbies(id),
  seq         INTEGER NOT NULL,
  type        TEXT    NOT NULL,
  occurred_at TEXT    NOT NULL,
  data        TEXT    NOT NULL,
  UNIQUE (lobby_id, seq)
);
