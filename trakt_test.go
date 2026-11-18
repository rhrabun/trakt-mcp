package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type apiCall struct {
	method string
	path   string
	body   any
}

type rule struct {
	needle   string
	status   int
	response string
}

func okRule(needle, response string) rule {
	return rule{needle: needle, status: 200, response: response}
}

// scripted returns a client whose first rule containing METHOD PATH wins, and a
// log of every call made.
func scripted(t *testing.T, rules ...rule) (*Client, *[]apiCall) {
	t.Helper()
	calls := []apiCall{}
	c := &Client{baseURL: "http://test", clientIDPath: "unused", tokenPath: "unused"}
	c.call = func(_ context.Context, method, path string, body any) (int, []byte, error) {
		calls = append(calls, apiCall{method: method, path: path, body: body})
		for _, r := range rules {
			if strings.Contains(method+" "+path, r.needle) {
				return r.status, []byte(r.response), nil
			}
		}
		t.Fatalf("unscripted call: %s %s", method, path)
		return 0, nil, nil
	}
	return c, &calls
}

func TestWatchedAtUnknownAndReleasedPassThrough(t *testing.T) {
	if got := normalizeWatchedAt("unknown"); got != "unknown" {
		t.Fatalf("unknown = %q", got)
	}
	if got := normalizeWatchedAt("released"); got != "released" {
		t.Fatalf("released = %q", got)
	}
}

func TestWatchedAtIsNormalisedToATimestamp(t *testing.T) {
	if got := normalizeWatchedAt("2026-09-10"); got != "2026-09-10T10:00:00.000Z" {
		t.Fatalf("date = %q", got)
	}
	if got := normalizeWatchedAt("2026-09-10T12:00:00.000Z"); got != "2026-09-10T12:00:00.000Z" {
		t.Fatalf("timestamp = %q", got)
	}
	if err := validateWatchedAt("garbage"); err == nil {
		t.Fatal("expected a date error")
	}
}

func TestRateMovieDoublesStarsToTraktRating(t *testing.T) {
	c, calls := scripted(t,
		okRule("GET /search/movie", `[{"movie":{"title":"X","year":2000,"ids":{"trakt":1}}}]`),
		rule{needle: "POST /sync/ratings", status: 201, response: `{"added":{"movies":1}}`},
	)
	if _, out, err := c.rateMovieTool(context.Background(), nil, RateInput{Title: "X", Stars: 4.5}); err != nil {
		t.Fatal(err)
	} else if out.TraktRating != 9 || !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if _, _, err := c.rateMovieTool(context.Background(), nil, RateInput{Title: "X", Stars: 0.5}); err != nil {
		t.Fatal(err)
	}

	first := (*calls)[1].body.(ratingsPayload).Movies[0].Rating
	second := (*calls)[3].body.(ratingsPayload).Movies[0].Rating
	if first != 9 || second != 1 {
		t.Fatalf("ratings = %d, %d", first, second)
	}
}

func TestRateMovieRejectsOutOfRangeStars(t *testing.T) {
	c, calls := scripted(t)
	for _, stars := range []float64{0.3, 5.2, 4.25} {
		if _, _, err := c.rateMovieTool(context.Background(), nil, RateInput{Title: "dummy", Stars: stars}); err == nil {
			t.Fatalf("stars %v accepted", stars)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("calls made: %d", len(*calls))
	}
}

func TestStatsExcludesTheUnknownDateMarkerFromYears(t *testing.T) {
	c, _ := scripted(t,
		okRule("GET /sync/watched/movies", `[
			{"movie":{"title":"Dated","year":2020,"ids":{"trakt":1}},
			 "last_watched_at":"2026-05-01T00:00:00.000Z"},
			{"movie":{"title":"Undated","year":1999,"ids":{"trakt":2}},
			 "last_watched_at":"1970-01-01T00:00:00.000Z"}
		]`),
		okRule("GET /users/me/ratings/movies", `[]`),
		okRule("GET /users/me/watchlist/movies", `[]`),
		okRule("GET /sync/watched/shows", `[]`),
	)
	_, out, err := c.statsTool(context.Background(), nil, StatsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.YearsCovered, []string{"2026"}) || out.UnknownDateMovies != 1 {
		t.Fatalf("out = %+v", out)
	}
}

func TestUpNextExtractsTheNextEpisodeAndSkipsFinished(t *testing.T) {
	c, calls := scripted(t, okRule("GET /sync/progress/watched", `[
		{"show":{"title":"S","year":2020,"ids":{"trakt":5}},
		 "progress":{"aired":10,"completed":3,"last_watched_at":"2026-01-01T00:00:00.000Z",
		 "next_episode":{"season":2,"number":1,"title":"E","ids":{"trakt":99}}}},
		{"show":{"title":"Done","year":2019,"ids":{"trakt":6}},
		 "progress":{"aired":10,"completed":10,"next_episode":null}}
	]`))
	_, out, err := c.upNextTool(context.Background(), nil, UpNextInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*calls)[0].path, "hide_completed=true") {
		t.Fatalf("path = %s", (*calls)[0].path)
	}
	if out.Count != 1 || len(out.Shows) != 1 {
		t.Fatalf("out = %+v", out)
	}
	next := out.Shows[0].NextEpisode
	if next == nil || next.Season == nil || *next.Season != 2 || next.Number == nil || *next.Number != 1 || next.TraktID != 99 {
		t.Fatalf("next = %+v", next)
	}
}

func TestWhenReportsTheUnknownMarkerAsNoDate(t *testing.T) {
	for _, value := range []string{"1970-01-01T00:00:00.000Z", "", "1970-01-01"} {
		text := value
		if got := cleanDate(&text); got != nil {
			t.Fatalf("%q = %v", value, *got)
		}
	}
	dated := "2026-09-25T10:00:00.000Z"
	if got := cleanDate(&dated); got == nil || *got != dated {
		t.Fatalf("dated = %v", got)
	}
}

func TestUpNextOrdersStartedShowsFirstAndDropsTheMarker(t *testing.T) {
	c, _ := scripted(t, okRule("GET /sync/progress/watched", `[
		{"show":{"title":"Backlog","year":1,"ids":{"trakt":1}},
		 "progress":{"aired":10,"completed":2,"last_watched_at":"1970-01-01T00:00:00.000Z",
		 "next_episode":{"season":1,"number":3,"ids":{"trakt":30}}}},
		{"show":{"title":"Nearly done","year":2,"ids":{"trakt":2}},
		 "progress":{"aired":10,"completed":9,
		 "next_episode":{"season":1,"number":10,"ids":{"trakt":31}}}},
		{"show":{"title":"Fresh","year":3,"ids":{"trakt":3}},
		 "progress":{"aired":5,"completed":0,
		 "next_episode":{"season":1,"number":1,"ids":{"trakt":32}}}}
	]`))
	_, out, err := c.upNextTool(context.Background(), nil, UpNextInput{})
	if err != nil {
		t.Fatal(err)
	}
	titles := []string{out.Shows[0].Title, out.Shows[1].Title, out.Shows[2].Title}
	if !reflect.DeepEqual(titles, []string{"Nearly done", "Backlog", "Fresh"}) {
		t.Fatalf("titles = %v", titles)
	}
	if out.Shows[1].LastWatchedAt != nil {
		t.Fatalf("marker not dropped: %v", *out.Shows[1].LastWatchedAt)
	}
}

type historyRow struct {
	Episode struct {
		Season int `json:"season"`
		Number int `json:"number"`
	} `json:"episode"`
}

func TestMarkSeasonWatchedSkipsEpisodesAlreadyRecorded(t *testing.T) {
	posted := false
	var sent historyPayload
	c := &Client{baseURL: "http://test", clientIDPath: "unused", tokenPath: "unused"}
	c.call = func(_ context.Context, method, path string, body any) (int, []byte, error) {
		switch {
		case path == "/search/show?query=S&limit=10":
			return 200, []byte(`[{"show":{"title":"S","year":2020,"ids":{"trakt":5}}}]`), nil
		case path == "/shows/5/seasons?extended=full,episodes":
			return 200, []byte(`[
				{"number":1,"aired_episodes":3,"episodes":[{"number":1},{"number":2},{"number":3}]},
				{"number":2,"aired_episodes":0,"episodes":[{"number":1},{"number":2}]}
			]`), nil
		case strings.HasPrefix(path, "/sync/history/shows/5"):
			seen := []int{1}
			if posted {
				seen = []int{1, 2, 3}
			}
			rows := make([]historyRow, 0, len(seen))
			for _, number := range seen {
				var row historyRow
				row.Episode.Season = 1
				row.Episode.Number = number
				rows = append(rows, row)
			}
			raw, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			return 200, raw, nil
		case path == "/sync/history":
			sent = body.(historyPayload)
			posted = true
			return 201, []byte(`{"added":{"episodes":2}}`), nil
		}
		t.Fatalf("unscripted call: %s %s", method, path)
		return 0, nil, nil
	}

	_, out, err := c.markSeasonTool(context.Background(), nil, MarkSeasonInput{Title: "S", Season: 1})
	if err != nil {
		t.Fatal(err)
	}
	episodes := sent.Shows[0].Seasons[0].Episodes
	if len(episodes) != 2 || episodes[0].Number != 2 || episodes[1].Number != 3 {
		t.Fatalf("posted = %+v", episodes)
	}
	if episodes[0].WatchedAt != "unknown" {
		t.Fatalf("watched_at = %q", episodes[0].WatchedAt)
	}
	if out.Added != 2 || out.AlreadyWatched != 1 || out.RecordedNow != 3 || out.Aired != 3 || !out.OK {
		t.Fatalf("out = %+v", out)
	}

	_, again, err := c.markSeasonTool(context.Background(), nil, MarkSeasonInput{Title: "S", Season: 1})
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.RecordedNow != 3 || !again.OK {
		t.Fatalf("again = %+v", again)
	}
}

func TestMarkSeasonWatchedRefusesASeasonWithNothingAired(t *testing.T) {
	c, _ := scripted(t,
		okRule("GET /search/show", `[{"show":{"title":"S","year":2020,"ids":{"trakt":5}}}]`),
		okRule("GET /shows/5/seasons", `[{"number":2,"aired_episodes":0,"episodes":[{"number":1},{"number":2}]}]`),
	)
	if _, _, err := c.markSeasonTool(context.Background(), nil, MarkSeasonInput{Title: "S", Season: 2}); err == nil || !strings.Contains(err.Error(), "no aired episodes") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := c.markSeasonTool(context.Background(), nil, MarkSeasonInput{Title: "S", Season: 9}); err == nil || !strings.Contains(err.Error(), "has no season 9") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchlistRejectsAnUnknownKind(t *testing.T) {
	c, calls := scripted(t)
	episode := "episode"
	if _, _, err := c.watchlistAddTool(context.Background(), nil, WatchlistInput{Title: "X", Kind: &episode}); err == nil {
		t.Fatal("expected a kind error")
	}
	season := "season"
	if _, _, err := c.watchlistRemoveTool(context.Background(), nil, WatchlistInput{Title: "X", Kind: &season}); err == nil {
		t.Fatal("expected a kind error")
	}
	if len(*calls) != 0 {
		t.Fatalf("calls made: %d", len(*calls))
	}
}

func TestExpiryUsesExpiresAtOrFallsBackToCreatedAt(t *testing.T) {
	if got := (token{ExpiresAt: 5000}).expiresAt(timeForTest()); got != 5000 {
		t.Fatalf("expires_at = %v", got)
	}
	if got := (token{CreatedAt: 1000, ExpiresIn: 3600}).expiresAt(timeForTest()); got != 4600 {
		t.Fatalf("created_at + expires_in = %v", got)
	}
}

func TestMoviesFlattenTheTraktShape(t *testing.T) {
	rows := []movieRow{{
		Movie:         item{Title: "Project X", Year: intPtr(2012), IDs: ids{Trakt: 40847}},
		LastWatchedAt: strPtr("2026-09-25T10:00:00.000Z"),
	}}
	got := toMovies(rows)
	want := []Movie{{
		Title:     "Project X",
		Year:      intPtr(2012),
		TraktID:   40847,
		WatchedAt: strPtr("2026-09-25T10:00:00.000Z"),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got = %+v", got)
	}
}

func TestShowOutputOmitsEmptyValues(t *testing.T) {
	c, _ := scripted(t, okRule("GET /sync/watched/shows", `[
		{"show":{"title":"S","year":2020,"ids":{"trakt":5}}}
	]`))
	_, out, err := c.watchedShowsTool(context.Background(), nil, LimitInput{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "plays") || strings.Contains(text, "last_watched_at") {
		t.Fatalf("empty values not omitted: %s", text)
	}
}

func TestSaveTokenWritesAtomicallyWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{tokenPath: path}
	if err := c.saveToken(token{AccessToken: "new", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o", perm)
	}
	saved, err := c.loadToken()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "new" || saved.RefreshToken != "r" {
		t.Fatalf("saved = %+v", saved)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestServerListsAllToolsWithSchemas(t *testing.T) {
	ctx := context.Background()
	client, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "trakt", Version: "test"}, nil)
	addTools(server, client)
	session := connectInMemory(t, ctx, server)
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 14 {
		t.Fatalf("tools = %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", tool.Name)
		}
	}
}

func TestInputSchemasPublishDefaults(t *testing.T) {
	ctx := context.Background()
	client, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "trakt", Version: "test"}, nil)
	addTools(server, client)
	session := connectInMemory(t, ctx, server)
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{
		"trakt_watched_movies":      {"limit": 1000.0},
		"trakt_movie_ratings":       {"limit": 1000.0},
		"trakt_watchlist":           {"limit": 1000.0},
		"trakt_watched_shows":       {"limit": 300.0},
		"trakt_up_next":             {"limit": 100.0, "include_completed": false},
		"trakt_search":              {"kind": "movie"},
		"trakt_mark_season_watched": {"watched_at": "unknown"},
		"trakt_watchlist_add":       {"kind": "movie"},
		"trakt_watchlist_remove":    {"kind": "movie"},
		"trakt_recommendations":     {"limit": 20.0},
	}

	checked := 0
	for _, tool := range list.Tools {
		expected, ok := want[tool.Name]
		if !ok {
			continue
		}
		checked++
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Default any `json:"default"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for property, value := range expected {
			if got := schema.Properties[property].Default; !reflect.DeepEqual(got, value) {
				t.Errorf("%s.%s default = %#v, want %#v", tool.Name, property, got, value)
			}
		}
	}
	if checked != len(want) {
		t.Fatalf("checked %d tools, want %d", checked, len(want))
	}
}

func TestToolCallReturnsStructuredContent(t *testing.T) {
	c, _ := scripted(t,
		okRule("GET /sync/watched/movies", `[{"movie":{"title":"X","year":2000,"ids":{"trakt":1}},
			"last_watched_at":"2026-01-01T00:00:00.000Z"}]`),
		okRule("GET /users/me/ratings/movies", `[]`),
		okRule("GET /users/me/watchlist/movies", `[]`),
		okRule("GET /sync/watched/shows", `[]`),
	)
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "trakt", Version: "test"}, nil)
	addTools(server, c)
	session := connectInMemory(t, ctx, server)
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "trakt_stats", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool failed: %v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["movies_watched"] != 1.0 {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
}

func connectInMemory(t *testing.T, ctx context.Context, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func intPtr(value int) *int {
	return &value
}

func strPtr(value string) *string {
	return &value
}

func timeForTest() time.Time {
	return time.Unix(0, 0)
}
