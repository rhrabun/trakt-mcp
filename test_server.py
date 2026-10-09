import json

import server
from server import (
    _expires_at,
    _movies,
    _norm_watched_at,
    _scrub,
    _when,
    trakt_rate_movie,
)


def test_watched_at_unknown_and_released_pass_through():
    assert _norm_watched_at("unknown") == "unknown"
    assert _norm_watched_at("released") == "released"


def test_rate_movie_doubles_stars_to_trakt_rating():
    # 4.5 stars must go out as 9, not 4 (Trakt truncates a half value silently).
    server._find = lambda kind, title, year=None: {"title": "X", "year": 2000, "ids": {"trakt": 1}}
    sent = {}

    def fake_request(method, path, body=None, retry=True):
        sent["rating"] = body["movies"][0]["rating"]
        return 200, {}

    server._request = fake_request
    trakt_rate_movie("X", 4.5)
    assert sent["rating"] == 9
    trakt_rate_movie("X", 0.5)
    assert sent["rating"] == 1


def test_rate_movie_rejects_out_of_range_stars():
    # 0.3 -> round(0.6) = 1 and 5.2 -> round(10.4) = 10 must still be rejected.
    assert "between 0.5 and 5" in trakt_rate_movie("dummy", 0.3)
    assert "between 0.5 and 5" in trakt_rate_movie("dummy", 5.2)


def test_stats_excludes_the_unknown_date_marker_from_years():
    def fake_request(method, path, body=None, retry=True):
        if "watched/movies" in path:
            return 200, [
                {
                    "movie": {"title": "Dated", "year": 2020, "ids": {"trakt": 1}},
                    "last_watched_at": "2026-05-01T00:00:00.000Z",
                },
                {
                    "movie": {"title": "Undated", "year": 1999, "ids": {"trakt": 2}},
                    "last_watched_at": "1970-01-01T00:00:00.000Z",
                },
            ]
        return 200, []

    server._request = fake_request
    out = json.loads(server.trakt_stats())
    assert out["years_covered"] == ["2026"]
    assert out["unknown_date_movies"] == 1


def test_up_next_extracts_the_next_episode_and_skips_finished():
    def fake_request(method, path, body=None, retry=True):
        assert path.startswith("/sync/progress/watched")
        assert "hide_completed=true" in path
        return 200, [
            {
                "show": {"title": "S", "year": 2020, "ids": {"trakt": 5}},
                "progress": {
                    "aired": 10,
                    "completed": 3,
                    "last_watched_at": "2026-01-01T00:00:00.000Z",
                    "next_episode": {"season": 2, "number": 1, "title": "E", "ids": {"trakt": 99}},
                },
            },
            {
                "show": {"title": "Done", "year": 2019, "ids": {"trakt": 6}},
                "progress": {"aired": 10, "completed": 10, "next_episode": None},
            },
        ]

    server._request = fake_request
    out = json.loads(server.trakt_up_next())
    assert out["count"] == 1
    assert out["shows"][0]["next_episode"] == {"season": 2, "number": 1, "title": "E", "trakt_id": 99}


def test_when_reports_the_unknown_marker_as_no_date():
    assert _when("1970-01-01T00:00:00.000Z") is None
    assert _when(None) is None
    assert _when("") is None
    assert _when("2026-09-25T10:00:00.000Z") == "2026-09-25T10:00:00.000Z"


def test_up_next_orders_started_shows_first_and_drops_the_marker():
    def fake_request(method, path, body=None, retry=True):
        return 200, [
            {
                "show": {"title": "Backlog", "year": 1, "ids": {"trakt": 1}},
                "progress": {"aired": 10, "completed": 2,
                             "last_watched_at": "1970-01-01T00:00:00.000Z",
                             "next_episode": {"season": 1, "number": 3, "ids": {"trakt": 30}}},
            },
            {
                "show": {"title": "Nearly done", "year": 2, "ids": {"trakt": 2}},
                "progress": {"aired": 10, "completed": 9,
                             "next_episode": {"season": 1, "number": 10, "ids": {"trakt": 31}}},
            },
            {
                "show": {"title": "Fresh", "year": 3, "ids": {"trakt": 3}},
                "progress": {"aired": 5, "completed": 0,
                             "next_episode": {"season": 1, "number": 1, "ids": {"trakt": 32}}},
            },
        ]

    server._request = fake_request
    out = json.loads(server.trakt_up_next())
    assert [s["title"] for s in out["shows"]] == ["Nearly done", "Backlog", "Fresh"]
    assert "last_watched_at" not in out["shows"][1]


def test_mark_season_watched_skips_episodes_already_recorded():
    server._find = lambda kind, title, year=None: {"title": "S", "year": 2020, "ids": {"trakt": 5}}
    posted = {}

    def fake_request(method, path, body=None, retry=True):
        if path.startswith("/shows/5/seasons"):
            return 200, [
                {"number": 1, "aired_episodes": 3, "episodes": [{"number": n} for n in (1, 2, 3)]},
                {"number": 2, "aired_episodes": 0, "episodes": [{"number": n} for n in (1, 2)]},
            ]
        if path.startswith("/sync/history/shows/5"):
            seen = [1, 2, 3] if posted else [1]
            return 200, [{"episode": {"season": 1, "number": n}} for n in seen]
        posted.update(body or {})
        return 200, {"added": {"episodes": 2}}

    server._request = fake_request
    out = json.loads(server.trakt_mark_season_watched("S", 1))
    sent = posted["shows"][0]["seasons"][0]["episodes"]
    assert [e["number"] for e in sent] == [2, 3]
    assert sent[0]["watched_at"] == "unknown"
    assert out["added"] == 2 and out["already_watched"] == 1
    # Reading back the season is what sets ok, not the accepted post.
    assert out["ok"] is True and out["recorded_now"] == 3
    again = json.loads(server.trakt_mark_season_watched("S", 1))
    assert again["added"] == 0 and again["ok"] is True


def test_mark_season_watched_refuses_a_season_with_nothing_aired():
    server._find = lambda kind, title, year=None: {"title": "S", "year": 2020, "ids": {"trakt": 5}}
    server._request = lambda method, path, body=None, retry=True: (
        200,
        [{"number": 2, "aired_episodes": 0, "episodes": [{"number": n} for n in (1, 2)]}],
    )
    assert "no aired episodes" in server.trakt_mark_season_watched("S", 2)
    assert "has no season 9" in server.trakt_mark_season_watched("S", 9)


def test_watchlist_rejects_an_unknown_kind():
    assert "kind must be" in server.trakt_watchlist_add("X", kind="episode")
    assert "kind must be" in server.trakt_watchlist_remove("X", kind="season")


def test_watched_at_is_normalised_to_a_timestamp():
    assert _norm_watched_at("2026-09-10") == "2026-09-10T10:00:00.000Z"
    assert _norm_watched_at("2026-09-10T12:00:00.000Z") == "2026-09-10T12:00:00.000Z"


def test_expiry_uses_expires_at_or_falls_back_to_created_at():
    assert _expires_at({"expires_at": 5000}) == 5000
    assert _expires_at({"created_at": 1000, "expires_in": 3600}) == 4600


def test_movies_flatten_the_trakt_shape():
    rows = [
        {
            "last_watched_at": "2026-09-25T10:00:00.000Z",
            "movie": {"title": "Project X", "year": 2012, "ids": {"trakt": 40847}},
        }
    ]
    assert _movies(rows) == [
        {
            "title": "Project X",
            "year": 2012,
            "trakt_id": 40847,
            "rated_at": None,
            "watched_at": "2026-09-25T10:00:00.000Z",
            "rating": None,
        }
    ]


def test_scrub_drops_empty_values():
    assert _scrub({"a": 1, "b": None, "c": "", "d": [], "e": {}}) == {"a": 1}


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_"):
            fn()
            print(f"{name}: OK")
    print("all passed")
