package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// inputSchema infers the input schema and publishes argument defaults, which
// jsonschema struct tags cannot express. The SDK validates provided schemas at
// AddTool time, so a bad default panics during startup, not on first call.
func inputSchema[T any](defaults map[string]any) *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("building the input schema for %T: %v", *new(T), err))
	}
	for name, value := range defaults {
		property, ok := schema.Properties[name]
		if !ok {
			panic(fmt.Sprintf("input schema for %T has no %q property", *new(T), name))
		}
		property.Default = mustJSON(value)
	}
	return schema
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func addTools(s *mcp.Server, c *Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_watched_movies",
		InputSchema: inputSchema[LimitInput](map[string]any{"limit": 1000}),
		Description: "Movies the account has watched, newest first: title, year, trakt_id, watched date. " +
			"Use this to read the account's film taste before recommending anything. limit defaults to 1000.",
	}, c.watchedMoviesTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_movie_ratings",
		InputSchema: inputSchema[LimitInput](map[string]any{"limit": 1000}),
		Description: "The account's movie ratings on Trakt, 1-10 (half-star ratings land on odd numbers). " +
			"Use alongside trakt_watched_movies to judge what the account likes. limit defaults to 1000.",
	}, c.movieRatingsTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_watchlist",
		InputSchema: inputSchema[LimitInput](map[string]any{"limit": 1000}),
		Description: "Movies on the account's Trakt watchlist (planned watches). limit defaults to 1000.",
	}, c.watchlistTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_watched_shows",
		InputSchema: inputSchema[LimitInput](map[string]any{"limit": 300}),
		Description: "TV series the account has watched, with episode counts and last-watched date. " +
			"Trakt covers series properly. limit defaults to 300.",
	}, c.watchedShowsTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_up_next",
		InputSchema: inputSchema[UpNextInput](map[string]any{"limit": 100, "include_completed": false}),
		Description: "The next unwatched episode for each show, one call for the whole account. " +
			"Ordered with shows you have already started first and fewest episodes left first, so the " +
			"nearest thing to finish is on top; unstarted shows come last. Set include_completed to " +
			"also list finished shows. limit defaults to 100.",
	}, c.upNextTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_search",
		InputSchema: inputSchema[SearchInput](map[string]any{"kind": "movie"}),
		Description: "Search Trakt for a movie or show. kind is 'movie' or 'show'. " +
			"Use to resolve a name to its trakt_id before any write.",
	}, c.searchTool)
	mcp.AddTool(s, &mcp.Tool{
		Name: "trakt_rate_movie",
		Description: "Rate a movie the account has watched. stars is a 0.5 to 5 scale " +
			"(converted to Trakt's 1-10 automatically). Confirms which film it matched.",
	}, c.rateMovieTool)
	mcp.AddTool(s, &mcp.Tool{
		Name: "trakt_rate_show",
		Description: "Rate a show. stars is a 0.5 to 5 scale (converted to Trakt's 1-10 automatically). " +
			"Ratings are per show, not per episode.",
	}, c.rateShowTool)
	mcp.AddTool(s, &mcp.Tool{
		Name: "trakt_mark_watched",
		Description: `Mark a movie as watched on a date. watched_at is YYYY-MM-DD, or "unknown" ` +
			"to mark it watched with no specific date. Send the same film twice to record a rewatch.",
	}, c.markWatchedTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_mark_season_watched",
		InputSchema: inputSchema[MarkSeasonInput](map[string]any{"watched_at": "unknown"}),
		Description: `Mark every aired episode of one season as watched, skipping episodes already on ` +
			`the account (posting one twice records a duplicate play). watched_at is YYYY-MM-DD or ` +
			`"unknown" (default). Call once per season to fill in a whole series.`,
	}, c.markSeasonTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_watchlist_add",
		InputSchema: inputSchema[WatchlistInput](map[string]any{"kind": "movie"}),
		Description: "Add a movie or show to the account's Trakt watchlist. kind is 'movie' or 'show'.",
	}, c.watchlistAddTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_watchlist_remove",
		InputSchema: inputSchema[WatchlistInput](map[string]any{"kind": "movie"}),
		Description: "Remove a movie or show from the account's Trakt watchlist. kind is 'movie' or 'show'.",
	}, c.watchlistRemoveTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trakt_recommendations",
		InputSchema: inputSchema[LimitInput](map[string]any{"limit": 20}),
		Description: "Trakt's own suggestions based on the account's history: title, year, trakt_id, overview. " +
			"limit defaults to 20.",
	}, c.recommendationsTool)
	mcp.AddTool(s, &mcp.Tool{
		Name: "trakt_stats",
		Description: "Counts for the Trakt account, computed from the raw lists because " +
			"Trakt's own /users/me/stats endpoint can return an empty body.",
	}, c.statsTool)
}

type LimitInput struct {
	Limit *int `json:"limit,omitempty" jsonschema:"maximum rows to fetch; the tool description gives the default"`
}

type Movie struct {
	Title     string  `json:"title"`
	Year      *int    `json:"year"`
	TraktID   int64   `json:"trakt_id"`
	RatedAt   *string `json:"rated_at"`
	WatchedAt *string `json:"watched_at"`
	Rating    *int    `json:"rating"`
}

type MovieListOutput struct {
	Count  int     `json:"count"`
	Movies []Movie `json:"movies"`
}

type RatingsOutput struct {
	Count   int     `json:"count"`
	Ratings []Movie `json:"ratings"`
}

type Show struct {
	Title         string  `json:"title,omitempty"`
	Year          *int    `json:"year,omitempty"`
	TraktID       int64   `json:"trakt_id,omitempty"`
	Plays         *int    `json:"plays,omitempty"`
	LastWatchedAt *string `json:"last_watched_at,omitempty"`
}

type ShowListOutput struct {
	Count int    `json:"count"`
	Shows []Show `json:"shows"`
}

type Episode struct {
	Season  *int   `json:"season,omitempty"`
	Number  *int   `json:"number,omitempty"`
	Title   string `json:"title,omitempty"`
	TraktID int64  `json:"trakt_id,omitempty"`
}

type UpNextShow struct {
	Title         string   `json:"title,omitempty"`
	Year          *int     `json:"year,omitempty"`
	TraktID       int64    `json:"trakt_id,omitempty"`
	Aired         *int     `json:"aired,omitempty"`
	Completed     *int     `json:"completed,omitempty"`
	LastWatchedAt *string  `json:"last_watched_at,omitempty"`
	NextEpisode   *Episode `json:"next_episode,omitempty"`
}

type UpNextOutput struct {
	Count int          `json:"count"`
	Shows []UpNextShow `json:"shows"`
}

type UpNextInput struct {
	Limit            *int  `json:"limit,omitempty" jsonschema:"maximum shows to fetch (default 100)"`
	IncludeCompleted *bool `json:"include_completed,omitempty" jsonschema:"also list shows with no unwatched episodes"`
}

type SearchInput struct {
	Query string  `json:"query" jsonschema:"movie or show title to search for"`
	Kind  *string `json:"kind,omitempty" jsonschema:"movie or show (default movie)"`
}

type SearchResult struct {
	Title    string `json:"title,omitempty"`
	Year     *int   `json:"year,omitempty"`
	TraktID  int64  `json:"trakt_id,omitempty"`
	Overview string `json:"overview,omitempty"`
}

type SearchOutput struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

type RateInput struct {
	Title string  `json:"title" jsonschema:"title to rate; call trakt_search first if unsure"`
	Stars float64 `json:"stars" jsonschema:"rating from 0.5 to 5, in half steps"`
	Year  *int    `json:"year,omitempty" jsonschema:"release year, to disambiguate a title"`
}

type RateOutput struct {
	OK          bool    `json:"ok"`
	Matched     string  `json:"matched"`
	TraktID     int64   `json:"trakt_id"`
	Stars       float64 `json:"stars"`
	TraktRating int     `json:"trakt_rating"`
	Response    any     `json:"response,omitempty"`
}

type MarkWatchedInput struct {
	Title     string `json:"title" jsonschema:"title to mark; call trakt_search first if unsure"`
	WatchedAt string `json:"watched_at" jsonschema:"YYYY-MM-DD, or unknown for a watch with no date"`
	Year      *int   `json:"year,omitempty" jsonschema:"release year, to disambiguate a title"`
}

type MarkWatchedOutput struct {
	OK        bool   `json:"ok"`
	Matched   string `json:"matched"`
	WatchedAt string `json:"watched_at"`
	Response  any    `json:"response,omitempty"`
}

type MarkSeasonInput struct {
	Title     string  `json:"title" jsonschema:"show title; call trakt_search first if unsure"`
	Season    int     `json:"season" jsonschema:"season number"`
	WatchedAt *string `json:"watched_at,omitempty" jsonschema:"YYYY-MM-DD or unknown (default unknown)"`
	Year      *int    `json:"year,omitempty" jsonschema:"release year, to disambiguate a title"`
}

type MarkSeasonOutput struct {
	OK             bool   `json:"ok"`
	Matched        string `json:"matched"`
	Season         int    `json:"season"`
	Added          int    `json:"added"`
	AlreadyWatched int    `json:"already_watched"`
	Aired          int    `json:"aired"`
	RecordedNow    int    `json:"recorded_now"`
	Response       any    `json:"response,omitempty"`
}

type WatchlistInput struct {
	Title string  `json:"title" jsonschema:"title to edit; call trakt_search first if unsure"`
	Year  *int    `json:"year,omitempty" jsonschema:"release year, to disambiguate a title"`
	Kind  *string `json:"kind,omitempty" jsonschema:"movie or show (default movie)"`
}

type WatchlistOutput struct {
	OK       bool   `json:"ok"`
	Matched  string `json:"matched"`
	Response any    `json:"response,omitempty"`
}

type Recommendation struct {
	Title    string   `json:"title,omitempty"`
	Year     *int     `json:"year,omitempty"`
	TraktID  int64    `json:"trakt_id,omitempty"`
	Rating   *float64 `json:"rating,omitempty"`
	Overview string   `json:"overview,omitempty"`
}

type RecommendationsOutput struct {
	Count           int              `json:"count"`
	Recommendations []Recommendation `json:"recommendations"`
}

type StatsInput struct{}

type StatsOutput struct {
	MoviesWatched     int      `json:"movies_watched"`
	MoviesRated       int      `json:"movies_rated"`
	AverageRating10   *float64 `json:"average_rating_10"`
	Watchlist         int      `json:"watchlist"`
	ShowsWatched      int      `json:"shows_watched"`
	YearsCovered      []string `json:"years_covered"`
	UnknownDateMovies int      `json:"unknown_date_movies"`
}

type movieRow struct {
	Movie         item    `json:"movie"`
	RatedAt       *string `json:"rated_at"`
	LastWatchedAt *string `json:"last_watched_at"`
	WatchedAt     *string `json:"watched_at"`
	Rating        *int    `json:"rating"`
}

type showRow struct {
	Show          item    `json:"show"`
	Plays         *int    `json:"plays"`
	LastWatchedAt *string `json:"last_watched_at"`
}

func toMovies(rows []movieRow) []Movie {
	movies := make([]Movie, 0, len(rows))
	for _, row := range rows {
		watched := row.LastWatchedAt
		if watched == nil || *watched == "" {
			watched = row.WatchedAt
		}
		movies = append(movies, Movie{
			Title:     row.Movie.Title,
			Year:      row.Movie.Year,
			TraktID:   row.Movie.IDs.Trakt,
			RatedAt:   row.RatedAt,
			WatchedAt: cleanDate(watched),
			Rating:    row.Rating,
		})
	}
	return movies
}

func (c *Client) watchedMoviesTool(ctx context.Context, _ *mcp.CallToolRequest, in LimitInput) (*mcp.CallToolResult, MovieListOutput, error) {
	var rows []movieRow
	path := fmt.Sprintf("/sync/watched/movies?limit=%d", valueOr(in.Limit, 1000))
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, MovieListOutput{}, err
	}
	movies := toMovies(rows)
	sort.SliceStable(movies, func(i, j int) bool {
		return valueOr(movies[i].WatchedAt, "") > valueOr(movies[j].WatchedAt, "")
	})
	return nil, MovieListOutput{Count: len(movies), Movies: movies}, nil
}

func (c *Client) movieRatingsTool(ctx context.Context, _ *mcp.CallToolRequest, in LimitInput) (*mcp.CallToolResult, RatingsOutput, error) {
	var rows []movieRow
	path := fmt.Sprintf("/users/me/ratings/movies?limit=%d", valueOr(in.Limit, 1000))
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, RatingsOutput{}, err
	}
	rated := toMovies(rows)
	sort.SliceStable(rated, func(i, j int) bool {
		return valueOr(rated[i].Rating, 0) > valueOr(rated[j].Rating, 0)
	})
	return nil, RatingsOutput{Count: len(rated), Ratings: rated}, nil
}

func (c *Client) watchlistTool(ctx context.Context, _ *mcp.CallToolRequest, in LimitInput) (*mcp.CallToolResult, MovieListOutput, error) {
	var rows []movieRow
	path := fmt.Sprintf("/users/me/watchlist/movies?limit=%d", valueOr(in.Limit, 1000))
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, MovieListOutput{}, err
	}
	movies := toMovies(rows)
	return nil, MovieListOutput{Count: len(movies), Movies: movies}, nil
}

func (c *Client) watchedShowsTool(ctx context.Context, _ *mcp.CallToolRequest, in LimitInput) (*mcp.CallToolResult, ShowListOutput, error) {
	var rows []showRow
	path := fmt.Sprintf("/sync/watched/shows?limit=%d&extended=full", valueOr(in.Limit, 300))
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, ShowListOutput{}, err
	}
	shows := make([]Show, 0, len(rows))
	for _, row := range rows {
		shows = append(shows, Show{
			Title:         row.Show.Title,
			Year:          row.Show.Year,
			TraktID:       row.Show.IDs.Trakt,
			Plays:         row.Plays,
			LastWatchedAt: cleanDate(row.LastWatchedAt),
		})
	}
	sort.SliceStable(shows, func(i, j int) bool {
		return valueOr(shows[i].LastWatchedAt, "") > valueOr(shows[j].LastWatchedAt, "")
	})
	return nil, ShowListOutput{Count: len(shows), Shows: shows}, nil
}

type progressRow struct {
	Show     item `json:"show"`
	Progress struct {
		Aired         *int     `json:"aired"`
		Completed     *int     `json:"completed"`
		LastWatchedAt *string  `json:"last_watched_at"`
		NextEpisode   *episode `json:"next_episode"`
	} `json:"progress"`
}

type episode struct {
	Season *int   `json:"season"`
	Number *int   `json:"number"`
	Title  string `json:"title"`
	IDs    ids    `json:"ids"`
}

func (c *Client) upNextTool(ctx context.Context, _ *mcp.CallToolRequest, in UpNextInput) (*mcp.CallToolResult, UpNextOutput, error) {
	includeCompleted := valueOr(in.IncludeCompleted, false)
	params := url.Values{}
	if limit := valueOr(in.Limit, 100); limit != 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if !includeCompleted {
		params.Set("hide_completed", "true")
	}
	path := "/sync/progress/watched"
	if query := params.Encode(); query != "" {
		path += "?" + query
	}

	var rows []progressRow
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, UpNextOutput{}, err
	}
	shows := make([]UpNextShow, 0, len(rows))
	for _, row := range rows {
		next := row.Progress.NextEpisode
		if next == nil && !includeCompleted {
			continue
		}
		shows = append(shows, UpNextShow{
			Title:         row.Show.Title,
			Year:          row.Show.Year,
			TraktID:       row.Show.IDs.Trakt,
			Aired:         row.Progress.Aired,
			Completed:     row.Progress.Completed,
			LastWatchedAt: cleanDate(row.Progress.LastWatchedAt),
			NextEpisode:   episodeOut(next),
		})
	}
	sort.SliceStable(shows, func(i, j int) bool {
		a, b := shows[i], shows[j]
		aUnstarted := a.Completed != nil && *a.Completed == 0
		bUnstarted := b.Completed != nil && *b.Completed == 0
		if aUnstarted != bUnstarted {
			return !aUnstarted
		}
		aLeft := valueOr(a.Aired, 0) - valueOr(a.Completed, 0)
		bLeft := valueOr(b.Aired, 0) - valueOr(b.Completed, 0)
		if aLeft != bLeft {
			return aLeft < bLeft
		}
		return a.Title < b.Title
	})
	return nil, UpNextOutput{Count: len(shows), Shows: shows}, nil
}

func episodeOut(e *episode) *Episode {
	if e == nil {
		return nil
	}
	return &Episode{Season: e.Season, Number: e.Number, Title: e.Title, TraktID: e.IDs.Trakt}
}

func (c *Client) searchTool(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	kind := valueOr(in.Kind, "movie")
	if kind != "movie" && kind != "show" {
		return nil, SearchOutput{}, errors.New("kind must be 'movie' or 'show'")
	}
	path := fmt.Sprintf("/search/%s?query=%s&limit=10", kind, url.QueryEscape(in.Query))
	var rows []struct {
		Movie *item `json:"movie"`
		Show  *item `json:"show"`
	}
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, SearchOutput{}, err
	}
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		found := row.Movie
		if kind == "show" {
			found = row.Show
		}
		if found == nil {
			continue
		}
		results = append(results, SearchResult{
			Title:    found.Title,
			Year:     found.Year,
			TraktID:  found.IDs.Trakt,
			Overview: truncateRunes(found.Overview, 200),
		})
	}
	return nil, SearchOutput{Query: in.Query, Results: results}, nil
}

type ratingsPayload struct {
	Movies []ratingEntry `json:"movies,omitempty"`
	Shows  []ratingEntry `json:"shows,omitempty"`
}

type ratingEntry struct {
	IDs    ids `json:"ids"`
	Rating int `json:"rating"`
}

func (c *Client) rateMovieTool(ctx context.Context, _ *mcp.CallToolRequest, in RateInput) (*mcp.CallToolResult, RateOutput, error) {
	return c.rate(ctx, "movie", in)
}

func (c *Client) rateShowTool(ctx context.Context, _ *mcp.CallToolRequest, in RateInput) (*mcp.CallToolResult, RateOutput, error) {
	return c.rate(ctx, "show", in)
}

func (c *Client) rate(ctx context.Context, kind string, in RateInput) (*mcp.CallToolResult, RateOutput, error) {
	if err := validateStars(in.Stars); err != nil {
		return nil, RateOutput{}, err
	}
	found, err := c.find(ctx, kind, in.Title, in.Year)
	if err != nil {
		return nil, RateOutput{}, fmt.Errorf("search failed: %w", err)
	}
	if found == nil {
		return nil, RateOutput{}, fmt.Errorf("no %s matched %q; call trakt_search", kind, in.Title)
	}
	traktRating := int(math.Round(in.Stars * 2))
	entry := ratingEntry{IDs: ids{Trakt: found.IDs.Trakt}, Rating: traktRating}
	payload := ratingsPayload{}
	if kind == "movie" {
		payload.Movies = []ratingEntry{entry}
	} else {
		payload.Shows = []ratingEntry{entry}
	}
	status, raw, err := c.post(ctx, "/sync/ratings", payload)
	if err != nil {
		return nil, RateOutput{}, err
	}
	if err := expectWrite("/sync/ratings", status, raw); err != nil {
		return nil, RateOutput{}, err
	}
	return nil, RateOutput{
		OK:          true,
		Matched:     label(found),
		TraktID:     found.IDs.Trakt,
		Stars:       in.Stars,
		TraktRating: traktRating,
		Response:    anyBody(raw),
	}, nil
}

type historyPayload struct {
	Movies []historyMovie `json:"movies,omitempty"`
	Shows  []historyShow  `json:"shows,omitempty"`
}

type historyMovie struct {
	IDs       ids    `json:"ids"`
	WatchedAt string `json:"watched_at"`
}

type historyShow struct {
	IDs     ids             `json:"ids"`
	Seasons []historySeason `json:"seasons"`
}

type historySeason struct {
	Number   int              `json:"number"`
	Episodes []historyEpisode `json:"episodes"`
}

type historyEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at"`
}

func (c *Client) markWatchedTool(ctx context.Context, _ *mcp.CallToolRequest, in MarkWatchedInput) (*mcp.CallToolResult, MarkWatchedOutput, error) {
	if err := validateWatchedAt(in.WatchedAt); err != nil {
		return nil, MarkWatchedOutput{}, err
	}
	movie, err := c.find(ctx, "movie", in.Title, in.Year)
	if err != nil {
		return nil, MarkWatchedOutput{}, fmt.Errorf("search failed: %w", err)
	}
	if movie == nil {
		return nil, MarkWatchedOutput{}, fmt.Errorf("no movie matched %q; call trakt_search", in.Title)
	}
	payload := historyPayload{Movies: []historyMovie{{
		IDs:       ids{Trakt: movie.IDs.Trakt},
		WatchedAt: normalizeWatchedAt(in.WatchedAt),
	}}}
	status, raw, err := c.post(ctx, "/sync/history", payload)
	if err != nil {
		return nil, MarkWatchedOutput{}, err
	}
	if err := expectWrite("/sync/history", status, raw); err != nil {
		return nil, MarkWatchedOutput{}, err
	}
	return nil, MarkWatchedOutput{
		OK:        true,
		Matched:   label(movie),
		WatchedAt: in.WatchedAt,
		Response:  anyBody(raw),
	}, nil
}

type season struct {
	Number        int  `json:"number"`
	AiredEpisodes *int `json:"aired_episodes"`
	Episodes      []struct {
		Number int `json:"number"`
	} `json:"episodes"`
}

func (c *Client) markSeasonTool(ctx context.Context, _ *mcp.CallToolRequest, in MarkSeasonInput) (*mcp.CallToolResult, MarkSeasonOutput, error) {
	watchedAt := valueOr(in.WatchedAt, "unknown")
	if err := validateWatchedAt(watchedAt); err != nil {
		return nil, MarkSeasonOutput{}, err
	}
	show, err := c.find(ctx, "show", in.Title, in.Year)
	if err != nil {
		return nil, MarkSeasonOutput{}, fmt.Errorf("search failed: %w", err)
	}
	if show == nil {
		return nil, MarkSeasonOutput{}, fmt.Errorf("no show matched %q; call trakt_search", in.Title)
	}
	showID := show.IDs.Trakt

	var seasons []season
	if err := c.getJSON(ctx, fmt.Sprintf("/shows/%d/seasons?extended=full,episodes", showID), &seasons); err != nil {
		return nil, MarkSeasonOutput{}, err
	}
	var chosen *season
	for i := range seasons {
		if seasons[i].Number == in.Season {
			chosen = &seasons[i]
			break
		}
	}
	if chosen == nil {
		return nil, MarkSeasonOutput{}, fmt.Errorf("%s has no season %d", show.Title, in.Season)
	}
	aired := valueOr(chosen.AiredEpisodes, 0)
	numbers := make([]int, 0, len(chosen.Episodes))
	for _, ep := range chosen.Episodes {
		numbers = append(numbers, ep.Number)
	}
	if len(numbers) > aired {
		numbers = numbers[:aired]
	}
	if len(numbers) == 0 {
		return nil, MarkSeasonOutput{}, fmt.Errorf("season %d has no aired episodes yet", in.Season)
	}

	already, err := c.watchedEpisodes(ctx, showID, in.Season)
	if err != nil {
		return nil, MarkSeasonOutput{}, err
	}
	todo := make([]historyEpisode, 0, len(numbers))
	for _, number := range numbers {
		if !already[number] {
			todo = append(todo, historyEpisode{Number: number, WatchedAt: normalizeWatchedAt(watchedAt)})
		}
	}
	if len(todo) == 0 {
		recordedNow := 0
		for _, number := range numbers {
			if already[number] {
				recordedNow++
			}
		}
		return nil, MarkSeasonOutput{
			OK:             true,
			Matched:        label(show),
			Season:         in.Season,
			Added:          0,
			AlreadyWatched: len(already),
			Aired:          aired,
			RecordedNow:    recordedNow,
		}, nil
	}

	payload := historyPayload{Shows: []historyShow{{
		IDs: ids{Trakt: showID},
		Seasons: []historySeason{{
			Number:   in.Season,
			Episodes: todo,
		}},
	}}}
	status, raw, err := c.post(ctx, "/sync/history", payload)
	if err != nil {
		return nil, MarkSeasonOutput{}, err
	}
	if err := expectWrite("/sync/history", status, raw); err != nil {
		return nil, MarkSeasonOutput{}, err
	}

	// An accepted post is not proof the plays are there.
	recorded, err := c.watchedEpisodes(ctx, showID, in.Season)
	if err != nil {
		return nil, MarkSeasonOutput{}, fmt.Errorf("posted %d episodes but could not verify the season: %w", len(todo), err)
	}
	recordedNow := 0
	for _, number := range numbers {
		if recorded[number] {
			recordedNow++
		}
	}
	return nil, MarkSeasonOutput{
		OK:             true,
		Matched:        label(show),
		Season:         in.Season,
		Added:          len(todo),
		AlreadyWatched: len(already),
		Aired:          aired,
		RecordedNow:    recordedNow,
		Response:       anyBody(raw),
	}, nil
}

// watchedEpisodes returns the episode numbers of one season that already carry
// a play. Trakt's watched-shows endpoint reports counts only, so play-level
// history is the source that answers this. Paged at 100 rows.
func (c *Client) watchedEpisodes(ctx context.Context, showID int64, season int) (map[int]bool, error) {
	found := map[int]bool{}
	for page := 1; page <= 60; page++ {
		var rows []struct {
			Episode struct {
				Season int `json:"season"`
				Number int `json:"number"`
			} `json:"episode"`
		}
		path := fmt.Sprintf("/sync/history/shows/%d?limit=100&page=%d", showID, page)
		if err := c.getJSON(ctx, path, &rows); err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if row.Episode.Season == season {
				found[row.Episode.Number] = true
			}
		}
		if len(rows) < 100 {
			break
		}
	}
	return found, nil
}

type idEntry struct {
	IDs ids `json:"ids"`
}

type watchlistPayload struct {
	Movies []idEntry `json:"movies,omitempty"`
	Shows  []idEntry `json:"shows,omitempty"`
}

func (c *Client) watchlistAddTool(ctx context.Context, _ *mcp.CallToolRequest, in WatchlistInput) (*mcp.CallToolResult, WatchlistOutput, error) {
	return c.editWatchlist(ctx, "add", in)
}

func (c *Client) watchlistRemoveTool(ctx context.Context, _ *mcp.CallToolRequest, in WatchlistInput) (*mcp.CallToolResult, WatchlistOutput, error) {
	return c.editWatchlist(ctx, "remove", in)
}

func (c *Client) editWatchlist(ctx context.Context, action string, in WatchlistInput) (*mcp.CallToolResult, WatchlistOutput, error) {
	kind := valueOr(in.Kind, "movie")
	if kind != "movie" && kind != "show" {
		return nil, WatchlistOutput{}, errors.New("kind must be 'movie' or 'show'")
	}
	found, err := c.find(ctx, kind, in.Title, in.Year)
	if err != nil {
		return nil, WatchlistOutput{}, fmt.Errorf("search failed: %w", err)
	}
	if found == nil {
		return nil, WatchlistOutput{}, fmt.Errorf("no %s matched %q; call trakt_search", kind, in.Title)
	}
	entry := idEntry{IDs: ids{Trakt: found.IDs.Trakt}}
	payload := watchlistPayload{}
	if kind == "movie" {
		payload.Movies = []idEntry{entry}
	} else {
		payload.Shows = []idEntry{entry}
	}
	path := "/sync/watchlist"
	if action == "remove" {
		path += "/remove"
	}
	status, raw, err := c.post(ctx, path, payload)
	if err != nil {
		return nil, WatchlistOutput{}, err
	}
	if err := expectWrite(path, status, raw); err != nil {
		return nil, WatchlistOutput{}, err
	}
	return nil, WatchlistOutput{OK: true, Matched: label(found), Response: anyBody(raw)}, nil
}

type recommendationRow struct {
	Title    string   `json:"title"`
	Year     *int     `json:"year"`
	IDs      ids      `json:"ids"`
	Rating   *float64 `json:"rating"`
	Overview string   `json:"overview"`
}

func (c *Client) recommendationsTool(ctx context.Context, _ *mcp.CallToolRequest, in LimitInput) (*mcp.CallToolResult, RecommendationsOutput, error) {
	var rows []recommendationRow
	path := fmt.Sprintf("/recommendations/movies?limit=%d", valueOr(in.Limit, 20))
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, RecommendationsOutput{}, err
	}
	out := make([]Recommendation, 0, len(rows))
	for _, row := range rows {
		out = append(out, Recommendation{
			Title:    row.Title,
			Year:     row.Year,
			TraktID:  row.IDs.Trakt,
			Rating:   row.Rating,
			Overview: truncateRunes(row.Overview, 200),
		})
	}
	return nil, RecommendationsOutput{Count: len(out), Recommendations: out}, nil
}

func (c *Client) statsTool(ctx context.Context, _ *mcp.CallToolRequest, _ StatsInput) (*mcp.CallToolResult, StatsOutput, error) {
	var watched, rated []movieRow
	var watchlist, shows []json.RawMessage
	sources := []struct {
		path string
		out  any
	}{
		{"/sync/watched/movies?limit=1000", &watched},
		{"/users/me/ratings/movies?limit=1000", &rated},
		{"/users/me/watchlist/movies?limit=1000", &watchlist},
		{"/sync/watched/shows?limit=1000", &shows},
	}
	for _, source := range sources {
		if err := c.getJSON(ctx, source.path, source.out); err != nil {
			return nil, StatsOutput{}, err
		}
	}

	movies := toMovies(watched)
	ratings := make([]int, 0, len(rated))
	for _, movie := range toMovies(rated) {
		if movie.Rating != nil {
			ratings = append(ratings, *movie.Rating)
		}
	}
	years := map[string]bool{}
	unknown := 0
	for _, movie := range movies {
		if movie.WatchedAt == nil {
			unknown++
			continue
		}
		if len(*movie.WatchedAt) >= 4 {
			years[(*movie.WatchedAt)[:4]] = true
		}
	}
	yearsCovered := make([]string, 0, len(years))
	for year := range years {
		yearsCovered = append(yearsCovered, year)
	}
	sort.Strings(yearsCovered)

	out := StatsOutput{
		MoviesWatched:     len(movies),
		MoviesRated:       len(ratings),
		Watchlist:         len(watchlist),
		ShowsWatched:      len(shows),
		YearsCovered:      yearsCovered,
		UnknownDateMovies: unknown,
	}
	if len(ratings) > 0 {
		sum := 0
		for _, rating := range ratings {
			sum += rating
		}
		average := math.Round(float64(sum)/float64(len(ratings))*100) / 100
		out.AverageRating10 = &average
	}
	return nil, out, nil
}

func validateStars(stars float64) error {
	if stars < 0.5 || stars > 5 {
		return errors.New("stars must be between 0.5 and 5")
	}
	if math.Abs(stars*2-math.Round(stars*2)) > 1e-9 {
		return errors.New("stars must be in 0.5 increments")
	}
	return nil
}

func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
