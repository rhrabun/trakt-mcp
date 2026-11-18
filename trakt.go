package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL = "https://api.trakt.tv"
	userAgent      = "trakt-mcp/0.1"
	apiVersion     = "2"

	requestTimeout = 45 * time.Second

	// Trakt stamps a watch with no date as 1970-01-01. That is not a viewing
	// date, so it is reported as no date and never sorts or counts as one.
	unknownDate = "1970-01-01"
)

// callFunc performs one authenticated API call. Tests replace it.
type callFunc func(ctx context.Context, method, path string, body any) (int, []byte, error)

// Client talks to the Trakt API.
type Client struct {
	clientIDPath string
	tokenPath    string
	baseURL      string
	http         *http.Client
	call         callFunc

	mu sync.Mutex
}

func NewClient() (*Client, error) {
	clientIDFile, err := resolvePath("TRAKT_CLIENT_ID_PATH", defaultClientIDPath)
	if err != nil {
		return nil, err
	}
	tokenFile, err := resolvePath("TRAKT_TOKEN_PATH", defaultTokenPath)
	if err != nil {
		return nil, err
	}
	c := &Client{
		clientIDPath: clientIDFile,
		tokenPath:    tokenFile,
		baseURL:      defaultBaseURL,
		http:         &http.Client{Timeout: requestTimeout},
	}
	c.call = c.do
	return c, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	status, raw, err := c.once(ctx, method, path, body)
	if err != nil {
		return 0, nil, err
	}
	if status != http.StatusUnauthorized {
		return status, raw, nil
	}
	if _, err := c.refresh(ctx); err != nil {
		return 0, nil, fmt.Errorf("refreshing token after 401: %w", err)
	}
	return c.once(ctx, method, path, body)
}

func (c *Client) once(ctx context.Context, method, path string, body any) (int, []byte, error) {
	accessToken, err := c.accessToken(ctx)
	if err != nil {
		return 0, nil, err
	}
	clientID, err := readTrimmed(c.clientIDPath)
	if err != nil {
		return 0, nil, fmt.Errorf("reading client id: %w", err)
	}

	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(data)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return 0, nil, err
	}
	for key, value := range baseHeaders(clientID) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp.StatusCode, raw, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	status, raw, err := c.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d: %s", path, status, snippet(raw))
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body any) (int, []byte, error) {
	return c.call(ctx, http.MethodPost, path, body)
}

func expectWrite(path string, status int, raw []byte) error {
	if status == http.StatusOK || status == http.StatusCreated {
		return nil
	}
	return fmt.Errorf("POST %s: HTTP %d: %s", path, status, snippet(raw))
}

type ids struct {
	Trakt int64 `json:"trakt"`
}

type item struct {
	Title    string `json:"title"`
	Year     *int   `json:"year"`
	IDs      ids    `json:"ids"`
	Overview string `json:"overview"`
}

// find resolves a title to a Trakt item. Exact title and year win; otherwise the
// first candidate that matches the year, otherwise nothing.
func (c *Client) find(ctx context.Context, kind, title string, year *int) (*item, error) {
	if kind != "movie" && kind != "show" {
		return nil, errors.New("kind must be 'movie' or 'show'")
	}
	path := fmt.Sprintf("/search/%s?query=%s&limit=10", kind, url.QueryEscape(title))
	var rows []struct {
		Movie *item `json:"movie"`
		Show  *item `json:"show"`
	}
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, err
	}

	want := strings.ToLower(strings.TrimSpace(title))
	var best *item
	for i := range rows {
		candidate := rows[i].Movie
		if kind == "show" {
			candidate = rows[i].Show
		}
		if candidate == nil {
			continue
		}
		exact := strings.ToLower(strings.TrimSpace(candidate.Title)) == want
		yearOK := year == nil || (candidate.Year != nil && *candidate.Year == *year)
		if exact && yearOK {
			return candidate, nil
		}
		if best == nil && yearOK {
			best = candidate
		}
	}
	return best, nil
}

func cleanDate(value *string) *string {
	if value == nil || *value == "" || strings.HasPrefix(*value, unknownDate) {
		return nil
	}
	return value
}

// normalizeWatchedAt keeps Trakt's "unknown" and "released" markers and full
// timestamps; a bare date becomes a timestamp at 10:00 UTC.
func normalizeWatchedAt(value string) string {
	if value == "unknown" || value == "released" || strings.Contains(value, "T") {
		return value
	}
	return value + "T10:00:00.000Z"
}

func validateWatchedAt(value string) error {
	if value == "unknown" || value == "released" || strings.Contains(value, "T") {
		return nil
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return fmt.Errorf(`watched_at must be YYYY-MM-DD, "unknown" or "released", got %q`, value)
	}
	return nil
}

func label(it *item) string {
	if it.Year == nil {
		return it.Title
	}
	return fmt.Sprintf("%s (%d)", it.Title, *it.Year)
}

func snippet(raw []byte) string {
	const limit = 400
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit])
}

// anyBody decodes a response body for pass-through in a tool result. Non-JSON
// bodies are returned as text.
func anyBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}
