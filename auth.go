// Package main is an MCP server for Trakt.
//
// It uses the OAuth token saved by the device-code login. The access token is
// refreshed when it is within an hour of expiring and retried once on a 401, so
// the server keeps working without manual steps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultClientIDPath = "~/.config/trakt/client_id"
	defaultTokenPath    = "~/.config/trakt/token.json"

	refreshWindow = time.Hour
	oauthTimeout  = 30 * time.Second
)

// token is the JSON saved by the OAuth login. expires_at is not always written,
// so expiresAt falls back to created_at + expires_in.
type token struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	TokenType    string  `json:"token_type"`
	Scope        string  `json:"scope"`
	CreatedAt    float64 `json:"created_at"`
	ExpiresIn    float64 `json:"expires_in"`
	ExpiresAt    float64 `json:"expires_at"`
}

func (t token) expiresAt(now time.Time) float64 {
	if t.ExpiresAt != 0 {
		return t.ExpiresAt
	}
	created := t.CreatedAt
	if created == 0 {
		created = float64(now.Unix())
	}
	return created + t.ExpiresIn
}

func resolvePath(env, fallback string) (string, error) {
	path := os.Getenv(env)
	if path == "" {
		path = fallback
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path, nil
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (c *Client) loadToken() (token, error) {
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return token{}, fmt.Errorf("reading token: %w", err)
	}
	var tok token
	if err := json.Unmarshal(data, &tok); err != nil {
		return token{}, fmt.Errorf("reading token: %w", err)
	}
	return tok, nil
}

// saveToken writes through a temporary file so a torn write cannot destroy the
// rotated refresh-token pair.
func (c *Client) saveToken(tok token) error {
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	tmp := c.tokenPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	if err := os.Rename(tmp, c.tokenPath); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	return nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tok, err := c.loadToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if tok.expiresAt(now)-float64(now.Unix()) >= refreshWindow.Seconds() {
		return tok.AccessToken, nil
	}
	fresh, err := c.refreshLocked(ctx)
	if err != nil {
		return "", err
	}
	return fresh.AccessToken, nil
}

func (c *Client) refresh(ctx context.Context) (token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshLocked(ctx)
}

func (c *Client) refreshLocked(ctx context.Context) (token, error) {
	tok, err := c.loadToken()
	if err != nil {
		return token{}, err
	}
	clientID, err := readTrimmed(c.clientIDPath)
	if err != nil {
		return token{}, fmt.Errorf("reading client id: %w", err)
	}
	body, err := json.Marshal(map[string]string{
		"refresh_token": tok.RefreshToken,
		"client_id":     clientID,
		"grant_type":    "refresh_token",
	})
	if err != nil {
		return token{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return token{}, err
	}
	for key, value := range baseHeaders(clientID) {
		req.Header.Set(key, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return token{}, fmt.Errorf("refreshing token: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return token{}, fmt.Errorf("refreshing token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return token{}, fmt.Errorf("refreshing token: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}

	var fresh token
	if err := json.Unmarshal(raw, &fresh); err != nil {
		return token{}, fmt.Errorf("refreshing token: %w", err)
	}
	if fresh.AccessToken == "" {
		return token{}, errors.New("refreshing token: no access token in the response")
	}
	if err := c.saveToken(fresh); err != nil {
		return token{}, err
	}
	return fresh, nil
}

func baseHeaders(clientID string) map[string]string {
	return map[string]string{
		"Content-Type":      "application/json",
		"User-Agent":        userAgent,
		"trakt-api-version": apiVersion,
		"trakt-api-key":     clientID,
	}
}
