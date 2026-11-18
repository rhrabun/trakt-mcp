# trakt-mcp

An MCP server that exposes a Trakt account to an agent: watch history, ratings,
watchlist, series and write access. Built with the
[official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).

## Credentials

Read from files whose paths come from the environment:

- `TRAKT_CLIENT_ID_PATH` (default `~/.config/trakt/client_id`) - the PKCE app's client id
  (no client secret exists).
- `TRAKT_TOKEN_PATH` (default `~/.config/trakt/token.json`) - OAuth token; refreshed
  automatically when it is within an hour of expiring, and retried once on a 401.

The token file is replaced atomically with mode 0600, and refreshes are serialised,
so concurrent tool calls cannot clobber a rotated token pair.

## Tools

| Tool | Purpose |
|------|---------|
| `trakt_watched_movies` | Movies watched, newest first, with dates |
| `trakt_movie_ratings` | Ratings 1-10 (half-stars land on odd numbers) |
| `trakt_watchlist` | Planned watches |
| `trakt_watched_shows` | Series with play counts and last-watched date |
| `trakt_up_next` | Next unwatched episode per show, one call for the account, nearest to finish first |
| `trakt_search` | Resolve a name to a `trakt_id` before writing |
| `trakt_rate_movie` / `trakt_rate_show` | Rate by title; take stars (0.5-5 in half steps) |
| `trakt_mark_watched` | Log a film on a date (or `unknown`); twice records a rewatch |
| `trakt_mark_season_watched` | Log a whole season, skipping episodes already on the account |
| `trakt_watchlist_add` / `trakt_watchlist_remove` | Watchlist edits, `kind` picks movie or show |
| `trakt_recommendations` | Trakt's own suggestions from the history |
| `trakt_stats` | Counts and average rating |

Tools return typed JSON in `structuredContent` as well as text content. A failed
call sets `isError` with the reason in the text, including the Trakt status and
response body.

## Notes

- `/users/me/stats` can return an empty body, so `trakt_stats` computes from the raw
  lists instead.
- Trakt ratings are 1-10 whole numbers. The write tools take stars and convert.
- A watch with no date is stored by Trakt as `1970-01-01T00:00:00.000Z` and is reported here
  as no date, never as a date, and never as a viewing year.
- Whole-season logging reads what is already on the account first: posting an episode twice
  records a duplicate play, which is why `trakt_mark_season_watched` skips those. It reads
  the season back afterwards, because an accepted post is not proof the plays are there.

## Setup

Requires Go 1.25 or newer.

```sh
go build -o trakt-mcp .   # the server binary
go test ./...             # self-check against a stubbed API client
go run ./cmd/smoke        # live end-to-end check over stdio, run from the repo root
```

## Registration

```json
{
  "mcpServers": {
    "trakt": {
      "command": "/path/to/trakt-mcp",
      "args": [],
      "env": {
        "TRAKT_CLIENT_ID_PATH": "/path/to/trakt_client_id",
        "TRAKT_TOKEN_PATH": "/path/to/trakt_token.json"
      }
    }
  }
}
```
