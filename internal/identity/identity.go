// SPDX-License-Identifier: AGPL-3.0-or-later

// Package identity resolves a student's Git-host username from an OAuth flow.
// This is the privacy-critical entry point: the student authenticates with the
// host, and the only identifier Cairn keeps is the username they already use
// publicly. No name, SIS ID, or plaintext email is requested or stored.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Resolver turns an OAuth authorization code into the authenticated user's
// host username.
type Resolver interface {
	Host() adapter.Host
	// AuthorizeURL is where the student is redirected to begin the OAuth flow.
	AuthorizeURL(state string) string
	// Resolve exchanges the callback code for a token and returns the username and
	// the host's stable numeric user id (as a string). The numeric id is used as
	// the durable identity anchor so that a renamed operator reuses the same row.
	Resolve(ctx context.Context, code string) (username, hostUserID string, err error)
}

// GitHub is a Resolver backed by GitHub's OAuth app flow.
type GitHub struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string

	// Endpoints; empty values fall back to public GitHub.
	AuthBase string // default https://github.com
	APIBase  string // default https://api.github.com

	HTTPClient *http.Client
}

// NewGitHub constructs a GitHub OAuth resolver.
func NewGitHub(clientID, clientSecret, redirectURL string) *GitHub {
	return &GitHub{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL}
}

// Compile-time guarantee that *GitHub satisfies Resolver.
var _ Resolver = (*GitHub)(nil)

func (g *GitHub) Host() adapter.Host { return adapter.HostGitHub }

func (g *GitHub) authBase() string {
	if g.AuthBase != "" {
		return strings.TrimRight(g.AuthBase, "/")
	}
	return "https://github.com"
}

func (g *GitHub) apiBase() string {
	if g.APIBase != "" {
		return strings.TrimRight(g.APIBase, "/")
	}
	return "https://api.github.com"
}

func (g *GitHub) client() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (g *GitHub) AuthorizeURL(state string) string {
	v := url.Values{}
	v.Set("client_id", g.ClientID)
	v.Set("redirect_uri", g.RedirectURL)
	v.Set("scope", "read:user")
	v.Set("state", state)
	return g.authBase() + "/login/oauth/authorize?" + v.Encode()
}

func (g *GitHub) Resolve(ctx context.Context, code string) (string, string, error) {
	token, err := g.exchange(ctx, code)
	if err != nil {
		return "", "", err
	}
	return g.username(ctx, token)
}

func (g *GitHub) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", g.RedirectURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.authBase()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity: token exchange status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("identity: no access token (%s)", out.Error)
	}
	return out.AccessToken, nil
}

// username calls the /user endpoint and returns (login, numericID, error).
// The numeric id is stable across renames and is used as the durable HostUserID.
func (g *GitHub) username(ctx context.Context, token string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiBase()+"/user", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := g.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("identity: GET /user status %d", resp.StatusCode)
	}
	var out struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", err
	}
	if out.Login == "" {
		return "", "", fmt.Errorf("identity: empty login")
	}
	if out.ID == 0 {
		return "", "", fmt.Errorf("identity: empty user id")
	}
	return out.Login, strconv.FormatInt(out.ID, 10), nil
}
