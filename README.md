# trakt-mcp

An MCP server that exposes a Trakt account to an agent: watch history, ratings,
watchlist, series and write access.

## Credentials

Read from files whose paths come from the environment:

- `TRAKT_CLIENT_ID_PATH` (default `~/.config/trakt/client_id`) - the PKCE app's client id
  (no client secret exists).
- `TRAKT_TOKEN_PATH` (default `~/.config/trakt/token.json`) - OAuth token; refreshed
  automatically when it is within an hour of expiring, and retried once on a 401.

## Tools

| Tool | Purpose |
|------|---------|
| `trakt_watched_movies` | Movies watched, newest first, with dates |
| `trakt_movie_ratings` | Ratings 1-10 (half-stars land on odd numbers) |
| `trakt_watchlist` | Planned watches |
| `trakt_watched_shows` | Series with play counts and last-watched date |
| `trakt_up_next` | Next unwatched episode per show, one call for the account, nearest to finish first |
| `trakt_search` | Resolve a name to a `trakt_id` before writing |
| `trakt_rate_movie` / `trakt_rate_show` | Rate by title; take stars (0.5-5) |
| `trakt_mark_watched` | Log a film on a date (or `unknown`); twice records a rewatch |
| `trakt_mark_season_watched` | Log a whole season, skipping episodes already on the account |
| `trakt_watchlist_add` / `trakt_watchlist_remove` | Watchlist edits, `kind` picks movie or show |
| `trakt_recommendations` | Trakt's own suggestions from the history |
| `trakt_stats` | Counts and average rating |

## Notes

- `/users/me/stats` can return an empty body, so `trakt_stats` computes from the raw
  lists instead.
- Trakt ratings are 1-10 whole numbers. The write tools take stars and convert.
- A watch with no date is stored by Trakt as `1970-01-01T00:00:00.000Z` and is reported here
  as no date, never as a date, and never as a viewing year.
- Whole-season logging reads what is already on the account first: posting an episode twice
  records a duplicate play, which is why `trakt_mark_season_watched` skips those.

## Setup

Requires [uv](https://docs.astral.sh/uv/).

```sh
uv sync
uv run python test_server.py   # self-check on the pure helpers
uv run python smoke.py         # live end-to-end check over stdio
```

## Registration

```json
{
  "mcpServers": {
    "trakt": {
      "command": "uv",
      "args": ["run", "--directory", "/path/to/trakt-mcp", "python", "server.py"],
      "env": {
        "TRAKT_CLIENT_ID_PATH": "/path/to/trakt_client_id",
        "TRAKT_TOKEN_PATH": "/path/to/trakt_token.json"
      }
    }
  }
}
```
