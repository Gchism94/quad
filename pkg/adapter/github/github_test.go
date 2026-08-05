// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(k),
	})
}

func TestNewAndHost(t *testing.T) {
	a, err := New(Config{AppID: 1, InstallationID: 2, PrivateKeyPEM: testKeyPEM(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.Host(); got != adapter.HostGitHub {
		t.Fatalf("Host() = %q, want %q", got, adapter.HostGitHub)
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(Config{AppID: 1, InstallationID: 2, PrivateKeyPEM: []byte("not a pem key")}); err == nil {
		t.Fatal("expected an error for an invalid private key, got nil")
	}
}

func TestAppJWTIsThreeSegments(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := appJWT(7, k)
	if err != nil {
		t.Fatalf("appJWT: %v", err)
	}
	dots := 0
	for _, c := range tok {
		if c == '.' {
			dots++
		}
	}
	if dots != 2 {
		t.Fatalf("JWT should have 2 dots (3 segments), got %d in %q", dots, tok)
	}
}

// TestCreateRepoFromTemplateReVerifyOn422 verifies that when the generate
// endpoint returns 422 (name already exists), CreateRepoFromTemplate confirms
// via RepoExists and returns the existing ref instead of an error.
func TestCreateRepoFromTemplateReVerifyOn422(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case calls == 1 && r.Method == http.MethodGet: // initial RepoExists → 404
			w.WriteHeader(http.StatusNotFound)
		case calls == 2 && r.Method == http.MethodPost: // generate → 422
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"name already exists on this account"}`))
		case calls == 3 && r.Method == http.MethodGet: // re-verify RepoExists → 200
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"hw1-alice"}`))
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	// Build an adapter wired to the test server (bypass App auth).
	a := &Adapter{httpc: srv.Client(), baseURL: srv.URL}

	ref, err := a.CreateRepoFromTemplate(
		context.Background(),
		adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "org", Name: "tmpl"},
		adapter.NamespaceRef{Host: adapter.HostGitHub, Slug: "org"},
		"hw1-alice",
		adapter.CreateRepoOptions{Private: true},
	)
	if err != nil {
		t.Fatalf("want nil error when repo confirmed existing, got: %v", err)
	}
	if ref.Name != "hw1-alice" || ref.Namespace != "org" {
		t.Fatalf("ref = %+v, want org/hw1-alice", ref)
	}
	if calls != 3 {
		t.Fatalf("want 3 calls (RepoExists + generate + re-verify), got %d", calls)
	}
}

func TestRepoWebURL(t *testing.T) {
	// github.com: the web host differs from the api.github.com API host.
	a := &Adapter{baseURL: defaultAPIBase}
	if got := a.RepoWebURL(adapter.RepoRef{Namespace: "cs-dept", Name: "hw1-alice"}); got != "https://github.com/cs-dept/hw1-alice" {
		t.Errorf("github.com web URL = %q", got)
	}
	// GHES: API base is https://host/api/v3 → web host is https://host.
	g := &Adapter{baseURL: "https://ghe.example.edu/api/v3"}
	if got := g.RepoWebURL(adapter.RepoRef{Namespace: "o", Name: "r"}); got != "https://ghe.example.edu/o/r" {
		t.Errorf("GHES web URL = %q", got)
	}
}
