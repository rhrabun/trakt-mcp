import server
from server import (
    _expires_at,
    _movies,
    _norm_watched_at,
    _scrub,
    trakt_rate_movie,
)


def test_watched_at_unknown_and_released_pass_through():
    assert _norm_watched_at("unknown") == "unknown"
    assert _norm_watched_at("released") == "released"


def test_rate_movie_doubles_stars_to_trakt_rating():
    # 4.5 stars must go out as 9, not 4 (Trakt truncates a half value silently).
    server._find_movie = lambda title, year=None: {"title": "X", "year": 2000, "ids": {"trakt": 1}}
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
