// SPDX-License-Identifier: AGPL-3.0-or-later

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

// GitLab is a Resolver backed by a GitLab instance's OAuth2 flow (gitlab.com or
// self-hosted). The token endpoint lives at {base}/oauth/token and the user
// endpoint at {base}/api/v4/user, authenticated with a Bearer access token.
//
// Privacy: only the user's username and numeric id are read from /api/v4/user;
// name, email, and all other fields are ignored.
type GitLab struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	BaseURL      string // instance root, e.g. https://gitlab.com
	HTTPClient   *http.Client
}

// NewGitLab constructs a GitLab OAuth resolver. BaseURL is the instance root
// (default https://gitlab.com); clientID and clientSecret are from the OAuth
// application registered on the instance.
func NewGitLab(clientID, clientSecret, redirectURL, baseURL string) *GitLab {
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	return &GitLab{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		BaseURL:      baseURL,
	}
}

// Compile-time guarantee that *GitLab satisfies Resolver.
var _ Resolver = (*GitLab)(nil)

func (g *GitLab) Host() adapter.Host { return adapter.HostGitLab }

func (g *GitLab) base() string { return strings.TrimRight(g.BaseURL, "/") }

func (g *GitLab) client() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// AuthorizeURL returns the GitLab OAuth2 authorization endpoint.
func (g *GitLab) AuthorizeURL(state string) string {
	v := url.Values{}
	v.Set("client_id", g.ClientID)
	v.Set("redirect_uri", g.RedirectURL)
	v.Set("response_type", "code")
	v.Set("scope", "read_user")
	v.Set("state", state)
	return g.base() + "/oauth/authorize?" + v.Encode()
}

// Resolve exchanges the authorization code for a token and returns the user's
// username and stable numeric id. Only username and id are read from the API.
func (g *GitLab) Resolve(ctx context.Context, code string) (string, string, error) {
	token, err := g.exchange(ctx, code)
	if err != nil {
		return "", "", err
	}
	return g.user(ctx, token)
}

func (g *GitLab) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", g.RedirectURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base()+"/oauth/token", strings.NewReader(form.Encode()))
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
		return "", fmt.Errorf("identity: gitlab token exchange status %d", resp.StatusCode)
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

// user calls /api/v4/user and returns (username, numericID, error). The numeric id
// is stable across username renames and is used as HostUserID.
func (g *GitLab) user(ctx context.Context, token string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base()+"/api/v4/user", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := g.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("identity: gitlab GET /user status %d", resp.StatusCode)
	}
	var out struct {
		Username string `json:"username"`
		ID       int64  `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", err
	}
	if out.Username == "" {
		return "", "", fmt.Errorf("identity: empty username")
	}
	if out.ID == 0 {
		return "", "", fmt.Errorf("identity: empty user id")
	}
	return out.Username, strconv.FormatInt(out.ID, 10), nil
}
