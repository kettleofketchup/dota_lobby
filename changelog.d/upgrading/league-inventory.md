The database gains `bot_leagues.first_seen_at` and a `bot_league_changes` table. The migration
runs by itself at startup; leagues already stored start with their last refresh time as first
seen, and no change is recorded for them. A database written by this release is refused by
older ones.
