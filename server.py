"""MCP server for Trakt: watch history, ratings, watchlist, series, writes.

Uses the OAuth token saved by the device-code login. The access token is refreshed
automatically when it is close to expiring, so this stays working without manual steps.
"""

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

from mcp.server import MCPServer

API = "https://api.trakt.tv"
CLIENT_ID_PATH = os.environ.get("TRAKT_CLIENT_ID_PATH", "trakt_client_id")
TOKEN_PATH = os.environ.get("TRAKT_TOKEN_PATH", "trakt_token.json")

mcp = MCPServer("trakt")


def _read(path: str) -> str:
    with open(path) as f:
        return f.read().strip()


def _client_id() -> str:
    return _read(CLIENT_ID_PATH)


def _save_token(token: dict) -> None:
    with open(TOKEN_PATH, "w") as f:
        json.dump(token, f)
    os.chmod(TOKEN_PATH, 0o600)


def _base_headers() -> dict:
    return {
        "Content-Type": "application/json",
        "User-Agent": "trakt-mcp/0.1",
        "trakt-api-version": "2",
        "trakt-api-key": _client_id(),
    }


def _expires_at(token: dict) -> float:
    if token.get("expires_at"):
        return float(token["expires_at"])
    return float(token.get("created_at", time.time())) + float(token.get("expires_in", 0))


def _refresh() -> dict:
    with open(TOKEN_PATH) as f:
        token = json.load(f)
    body = {
        "refresh_token": token["refresh_token"],
        "client_id": _client_id(),
        "grant_type": "refresh_token",
    }
    req = urllib.request.Request(
        API + "/oauth/token", data=json.dumps(body).encode(), headers=_base_headers()
    )
    with urllib.request.urlopen(req, timeout=30) as f:
        fresh = json.loads(f.read().decode())
    _save_token(fresh)
    return fresh


def _access_token() -> str:
    with open(TOKEN_PATH) as f:
        token = json.load(f)
    # Refresh early so a long-running call never dies mid-flight.
    if _expires_at(token) - time.time() < 3600:
        token = _refresh()
    return token["access_token"]


def _request(method: str, path: str, body: dict | None = None, retry: bool = True):
    headers = _base_headers()
    headers["Authorization"] = "Bearer " + _access_token()
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=45) as f:
            raw = f.read().decode()
            return f.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        if e.code == 401 and retry:
            _refresh()
            return _request(method, path, body, retry=False)
        return e.code, e.read().decode()[:400]
    except (urllib.error.URLError, ValueError) as e:
        return 0, f"request failed: {str(e)[:400]}"


def _movies(rows: list) -> list[dict]:
    if not isinstance(rows, list):
        return []
    out = []
    for r in rows:
        m = r.get("movie") or {}
        out.append(
            {
                "title": m.get("title"),
                "year": m.get("year"),
                "trakt_id": (m.get("ids") or {}).get("trakt"),
                "rated_at": r.get("rated_at"),
                "watched_at": r.get("last_watched_at") or r.get("watched_at"),
                "rating": r.get("rating"),
            }
        )
    return out


def _scrub(d: dict) -> dict:
    """Drop empty values so tool output stays readable."""
    return {k: v for k, v in d.items() if v not in (None, "", [], {})}


def _find_movie(title: str, year: int | None = None) -> dict | None:
    # ponytail: naive title match, best of 10. If a wrong film ever gets rated,
    # switch to requiring a trakt_id from trakt_search instead of a title.
    query = urllib.parse.quote(title)
    status, rows = _request("GET", f"/search/movie?query={query}&limit=10")
    if status != 200 or not rows:
        return None
    best = None
    for row in rows:
        m = row.get("movie") or {}
        exact = (m.get("title") or "").strip().lower() == title.strip().lower()
        year_ok = year is None or m.get("year") == year
        if exact and year_ok:
            return m
        if best is None and year_ok:
            best = m
    return best


def _norm_watched_at(value: str) -> str:
    if "T" in value:
        return value
    return f"{value}T10:00:00.000Z"


@mcp.tool()
def trakt_watched_movies(limit: int = 1000) -> str:
    """Movies the account has watched, newest first: title, year, trakt_id, watched date.
    Use this to read the account's film taste before recommending anything."""
    status, rows = _request("GET", f"/sync/watched/movies?limit={limit}")
    if status != 200:
        return json.dumps({"error": status, "detail": rows})
    movies = _movies(rows)
    movies.sort(key=lambda m: m.get("watched_at") or "", reverse=True)
    return json.dumps({"count": len(movies), "movies": movies}, indent=1)


@mcp.tool()
def trakt_movie_ratings(limit: int = 1000) -> str:
    """The account's movie ratings on Trakt, 1-10 (half-star ratings land on odd
    numbers). Use alongside trakt_watched_movies to judge what the account likes."""
    status, rows = _request("GET", f"/users/me/ratings/movies?limit={limit}")
    if status != 200:
        return json.dumps({"error": status, "detail": rows})
    rated = _movies(rows)
    rated.sort(key=lambda m: m.get("rating") or 0, reverse=True)
    return json.dumps({"count": len(rated), "ratings": rated}, indent=1)


@mcp.tool()
def trakt_watchlist(limit: int = 1000) -> str:
    """Movies on the account's Trakt watchlist (planned watches)."""
    status, rows = _request("GET", f"/users/me/watchlist/movies?limit={limit}")
    if status != 200:
        return json.dumps({"error": status, "detail": rows})
    return json.dumps({"count": len(rows or []), "movies": _movies(rows)}, indent=1)


@mcp.tool()
def trakt_watched_shows(limit: int = 300) -> str:
    """TV series the account has watched, with episode counts and last-watched date.
    Trakt covers series properly."""
    status, rows = _request("GET", f"/sync/watched/shows?limit={limit}&extended=full")
    if status != 200:
        return json.dumps({"error": status, "detail": rows})
    shows = []
    for r in rows or []:
        s = r.get("show") or {}
        shows.append(
            _scrub(
                {
                    "title": s.get("title"),
                    "year": s.get("year"),
                    "trakt_id": (s.get("ids") or {}).get("trakt"),
                    "plays": r.get("plays"),
                    "episodes": sum(
                        len(season.get("episodes") or []) for season in (r.get("seasons") or [])
                    )
                    or None,
                    "last_watched_at": r.get("last_watched_at"),
                }
            )
        )
    shows.sort(key=lambda s: s.get("last_watched_at") or "", reverse=True)
    return json.dumps({"count": len(shows), "shows": shows}, indent=1)


@mcp.tool()
def trakt_search(query: str, kind: str = "movie") -> str:
    """Search Trakt for a movie or show. kind is 'movie' or 'show'.
    Use to resolve a name to its trakt_id before any write."""
    if kind not in ("movie", "show"):
        return json.dumps({"error": "kind must be 'movie' or 'show'"})
    status, rows = _request("GET", f"/search/{kind}?query={urllib.parse.quote(query)}&limit=10")
    if status != 200:
        return json.dumps({"error": status, "detail": rows})
    results = []
    for r in rows or []:
        item = r.get(kind) or {}
        results.append(
            _scrub(
                {
                    "title": item.get("title"),
                    "year": item.get("year"),
                    "trakt_id": (item.get("ids") or {}).get("trakt"),
                    "overview": (item.get("overview") or "")[:200],
                }
            )
        )
    return json.dumps({"query": query, "results": results}, indent=1)


@mcp.tool()
def trakt_rate_movie(title: str, stars: float, year: int | None = None) -> str:
    """Rate a movie the account has watched. stars is a 0.5 to 5 scale
    (converted to Trakt's 1-10 automatically). Confirms which film it matched."""
    if not 0.5 <= stars <= 5:
        return json.dumps({"error": "stars must be between 0.5 and 5"})
    movie = _find_movie(title, year)
    if not movie:
        return json.dumps({"error": f"no movie matched {title!r}", "hint": "call trakt_search"})
    rating = round(stars * 2)
    status, body = _request(
        "POST",
        "/sync/ratings",
        {"movies": [{"ids": {"trakt": movie["ids"]["trakt"]}, "rating": rating}]},
    )
    return json.dumps(
        {
            "ok": status in (200, 201),
            "matched": f"{movie.get('title')} ({movie.get('year')})",
            "trakt_id": movie["ids"]["trakt"],
            "stars": stars,
            "trakt_rating": rating,
            "response": body,
        },
        indent=1,
    )


@mcp.tool()
def trakt_mark_watched(title: str, watched_at: str, year: int | None = None) -> str:
    """Mark a movie as watched on a date. watched_at is YYYY-MM-DD.
    Send the same film twice to record a rewatch."""
    movie = _find_movie(title, year)
    if not movie:
        return json.dumps({"error": f"no movie matched {title!r}", "hint": "call trakt_search"})
    status, body = _request(
        "POST",
        "/sync/history",
        {
            "movies": [
                {
                    "ids": {"trakt": movie["ids"]["trakt"]},
                    "watched_at": _norm_watched_at(watched_at),
                }
            ]
        },
    )
    return json.dumps(
        {
            "ok": status in (200, 201),
            "matched": f"{movie.get('title')} ({movie.get('year')})",
            "watched_at": watched_at,
            "response": body,
        },
        indent=1,
    )


@mcp.tool()
def trakt_watchlist_add(title: str, year: int | None = None) -> str:
    """Add a movie to the account's Trakt watchlist."""
    movie = _find_movie(title, year)
    if not movie:
        return json.dumps({"error": f"no movie matched {title!r}", "hint": "call trakt_search"})
    status, body = _request("POST", "/sync/watchlist", {"movies": [{"ids": {"trakt": movie["ids"]["trakt"]}}]})
    return json.dumps(
        {"ok": status in (200, 201), "matched": f"{movie.get('title')} ({movie.get('year')})", "response": body},
        indent=1,
    )


@mcp.tool()
def trakt_watchlist_remove(title: str, year: int | None = None) -> str:
    """Remove a movie from the account's Trakt watchlist."""
    movie = _find_movie(title, year)
    if not movie:
        return json.dumps({"error": f"no movie matched {title!r}", "hint": "call trakt_search"})
    status, body = _request(
        "POST", "/sync/watchlist/remove", {"movies": [{"ids": {"trakt": movie["ids"]["trakt"]}}]}
    )
    return json.dumps(
        {"ok": status in (200, 201), "matched": f"{movie.get('title')} ({movie.get('year')})", "response": body},
        indent=1,
    )


@mcp.tool()
def trakt_recommendations(limit: int = 20) -> str:
    """Trakt's own suggestions based on the account's history: title, year, trakt_id, overview."""
    status, rows = _request("GET", f"/recommendations/movies?limit={limit}")
    if status != 200:
        return json.dumps({"error": status, "detail": rows})
    out = [
        _scrub(
            {
                "title": (r or {}).get("title"),
                "year": (r or {}).get("year"),
                "trakt_id": ((r or {}).get("ids") or {}).get("trakt"),
                "rating": (r or {}).get("rating"),
                "overview": ((r or {}).get("overview") or "")[:200],
            }
        )
        for r in (rows or [])
    ]
    return json.dumps({"count": len(out), "recommendations": out}, indent=1)


@mcp.tool()
def trakt_stats() -> str:
    """Counts for the Trakt account, computed from the raw lists because
    Trakt's own /users/me/stats endpoint can return an empty body."""
    _, watched = _request("GET", "/sync/watched/movies?limit=1000")
    _, rated = _request("GET", "/users/me/ratings/movies?limit=1000")
    _, watchlist = _request("GET", "/users/me/watchlist/movies?limit=1000")
    _, shows = _request("GET", "/sync/watched/shows?limit=1000")
    movies = _movies(watched or [])
    ratings = [m["rating"] for m in _movies(rated or []) if m.get("rating")]
    years = sorted({m["watched_at"][:4] for m in movies if m.get("watched_at")})
    return json.dumps(
        {
            "movies_watched": len(movies),
            "movies_rated": len(ratings),
            "average_rating_10": round(sum(ratings) / len(ratings), 2) if ratings else None,
            "watchlist": len(watchlist or []),
            "shows_watched": len(shows or []),
            "years_covered": years,
        },
        indent=1,
    )


if __name__ == "__main__":
    mcp.run()
