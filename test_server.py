from server import (
    _expires_at,
    _movies,
    _norm_watched_at,
    _scrub,
    trakt_rate_movie,
)


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
