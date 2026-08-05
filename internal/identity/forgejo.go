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

// Forgejo is a Resolver backed by a Forgejo or Gitea instance's OAuth2 flow.
// It mirrors the GitHub resolver but handles two Gitea-specific requirements:
// the authorize endpoint requires response_type=code and the token exchange
// requires grant_type=authorization_code.
//
// Privacy: only the user's login (username) and numeric id are read from the
// /api/v1/user endpoint; name, email, bio, and all other fields are ignored.
type Forgejo struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	BaseURL      string // instance root, e.g. https://forgejo.example.org
	HTTPClient   *http.Client
	// HostValue is the host this resolver reports from Host(). The Gitea OAuth2
	// endpoints are identical, so a Gitea resolver differs only by this label
	// (the resolvers-map key and the host stamped on the student claim). Empty
	// defaults to adapter.HostForgejo.
	HostValue adapter.Host
}

// NewForgejo constructs a Forgejo OAuth resolver. BaseURL is the instance root
// (e.g. https://forgejo.example.org); clientID and clientSecret are from the
// OAuth2 application registered on the instance.
//
// To target Gitea (so claims and the resolvers-map key carry the "gitea" host),
// use NewForgejoWithHost.
func NewForgejo(clientID, clientSecret, redirectURL, baseURL string) *Forgejo {
	return NewForgejoWithHost(clientID, clientSecret, redirectURL, baseURL, adapter.HostForgejo)
}

// NewForgejoWithHost constructs a Gitea-family OAuth resolver that reports the
// given host. host must be adapter.HostForgejo or adapter.HostGitea — the OAuth2
// flow is identical; only the reported host differs.
func NewForgejoWithHost(clientID, clientSecret, redirectURL, baseURL string, host adapter.Host) *Forgejo {
	if host == "" {
		host = adapter.HostForgejo
	}
	return &Forgejo{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		BaseURL:      baseURL,
		HostValue:    host,
	}
}

// Compile-time guarantee that *Forgejo satisfies Resolver.
var _ Resolver = (*Forgejo)(nil)

// Host returns the host this resolver was constructed for (Forgejo or Gitea),
// defaulting to Forgejo when unset.
func (f *Forgejo) Host() adapter.Host {
	if f.HostValue == "" {
		return adapter.HostForgejo
	}
	return f.HostValue
}

func (f *Forgejo) base() string { return strings.TrimRight(f.BaseURL, "/") }

func (f *Forgejo) apiBase() string { return f.base() + "/api/v1" }

func (f *Forgejo) client() *http.Client {
	if f.HTTPClient != nil {
		return f.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// AuthorizeURL returns the Forgejo OAuth2 authorization endpoint. Unlike GitHub,
// Gitea requires response_type=code in the authorize request.
func (f *Forgejo) AuthorizeURL(state string) string {
	v := url.Values{}
	v.Set("client_id", f.ClientID)
	v.Set("redirect_uri", f.RedirectURL)
	v.Set("response_type", "code") // required by Gitea; GitHub omits this
	v.Set("scope", "read:user")
	v.Set("state", state)
	return f.base() + "/login/oauth/authorize?" + v.Encode()
}

// Resolve exchanges the authorization code for a token and returns the user's
// login and stable numeric id. Only the login and id fields are read from the
// Forgejo API; no name, email, or other personal data is requested or stored.
func (f *Forgejo) Resolve(ctx context.Context, code string) (string, string, error) {
	token, err := f.exchange(ctx, code)
	if err != nil {
		return "", "", err
	}
	return f.user(ctx, token)
}

func (f *Forgejo) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", f.ClientID)
	form.Set("client_secret", f.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", f.RedirectURL)
	form.Set("grant_type", "authorization_code") // required by Gitea; GitHub omits this

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.base()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := f.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identity: forgejo token exchange status %d", resp.StatusCode)
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

// user calls /api/v1/user and returns (login, numericID, error).
// The numeric id is stable across username renames and is used as HostUserID.
func (f *Forgejo) user(ctx context.Context, token string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.apiBase()+"/user", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token) // OAuth2 access tokens use Bearer, not token
	req.Header.Set("Accept", "application/json")

	resp, err := f.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("identity: forgejo GET /user status %d", resp.StatusCode)
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
