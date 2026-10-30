package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbao/openbao/sdk/v2/logical"
)

const testAdminToken = "admin-secret"

// fakeGitea serves the subset of the Gitea user token API the engine calls.
type fakeGitea struct {
	mu     sync.Mutex
	nextID int64
	tokens map[string][]accessToken
}

func newFakeGitea(t *testing.T) (*fakeGitea, *httptest.Server) {
	f := &fakeGitea{tokens: map[string][]accessToken{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitea) serve(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != testAdminToken || pass != "" {
		http.Error(w, "auth required", http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/users/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[1] != "tokens" || r.Header.Get("Sudo") != parts[0] {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := parts[0]

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && len(parts) == 2:
		var in accessToken
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, t := range f.tokens[username] {
			if t.Name == in.Name {
				http.Error(w, "access token name has been used already", http.StatusBadRequest)
				return
			}
		}
		f.nextID++
		tok := accessToken{ID: f.nextID, Name: in.Name, Scopes: in.Scopes, SHA1: "sha-" + in.Name}
		f.tokens[username] = append(f.tokens[username], tok)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(tok)
	case r.Method == http.MethodGet && len(parts) == 2:
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		all := f.tokens[username]
		start := min((page-1)*limit, len(all))
		out := []accessToken{}
		for _, t := range all[start:min(start+limit, len(all))] {
			out = append(out, accessToken{ID: t.ID, Name: t.Name, Scopes: t.Scopes})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodDelete && len(parts) == 3:
		toks := f.tokens[username]
		for i, t := range toks {
			if t.Name == parts[2] || strconv.FormatInt(t.ID, 10) == parts[2] {
				f.tokens[username] = append(toks[:i], toks[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (f *fakeGitea) add(username, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.tokens[username] = append(f.tokens[username], accessToken{ID: f.nextID, Name: name})
}

func (f *fakeGitea) names(username string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, t := range f.tokens[username] {
		out = append(out, t.Name)
	}
	return out
}

func newTestBackend(t *testing.T, giteaURL string) (*backend, logical.Storage) {
	t.Helper()
	cfg := logical.TestBackendConfig()
	cfg.StorageView = &logical.InmemStorage{}
	cfg.System = &logical.StaticSystemView{DefaultLeaseTTLVal: time.Hour, MaxLeaseTTLVal: 24 * time.Hour}
	b := newBackend()
	if err := b.Setup(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if giteaURL != "" {
		mustRequest(t, b, cfg.StorageView, logical.UpdateOperation, "config", map[string]any{
			"base_url": giteaURL,
			"token":    testAdminToken,
		})
		mustRequest(t, b, cfg.StorageView, logical.UpdateOperation, "roles/writer", map[string]any{
			"username": "bot",
			"scopes":   "write:repository,read:user",
			"ttl":      "1h",
			"max_ttl":  "3h",
		})
	}
	return b, cfg.StorageView
}

func request(b *backend, s logical.Storage, op logical.Operation, path string, data map[string]any) (*logical.Response, error) {
	return b.HandleRequest(context.Background(), &logical.Request{
		Operation: op,
		Path:      path,
		Data:      data,
		Storage:   s,
	})
}

func mustRequest(t *testing.T, b *backend, s logical.Storage, op logical.Operation, path string, data map[string]any) *logical.Response {
	t.Helper()
	resp, err := request(b, s, op, path, data)
	if err != nil {
		t.Fatalf("%s %s: %v", op, path, err)
	}
	if resp != nil && resp.IsError() {
		t.Fatalf("%s %s: %v", op, path, resp.Error())
	}
	return resp
}

func secretRequest(b *backend, s logical.Storage, op logical.Operation, secret *logical.Secret) (*logical.Response, error) {
	return b.HandleRequest(context.Background(), &logical.Request{
		Operation: op,
		Secret:    secret,
		Storage:   s,
	})
}

func TestConfigNeverReturnsToken(t *testing.T) {
	_, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)

	resp := mustRequest(t, b, s, logical.ReadOperation, "config", nil)
	if _, ok := resp.Data["token"]; ok {
		t.Fatal("config read returned the admin token")
	}
	if resp.Data["base_url"] != srv.URL || resp.Data["token_prefix"] != defaultTokenPrefix {
		t.Fatalf("unexpected config: %v", resp.Data)
	}
}

func TestConfigRejectsRelativeURL(t *testing.T) {
	b, s := newTestBackend(t, "")
	resp, err := request(b, s, logical.UpdateOperation, "config", map[string]any{"base_url": "gitea", "token": "x"})
	if err != nil || !resp.IsError() {
		t.Fatalf("expected an error response, got %v, %v", resp, err)
	}
}

func TestRoleRejectsTTLAboveMax(t *testing.T) {
	b, s := newTestBackend(t, "")
	resp, err := request(b, s, logical.UpdateOperation, "roles/r", map[string]any{
		"username": "u", "scopes": "read:user", "ttl": "2h", "max_ttl": "1h",
	})
	if err != nil || !resp.IsError() {
		t.Fatalf("expected an error response, got %v, %v", resp, err)
	}
}

func TestCredsCreatesToken(t *testing.T) {
	f, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)

	resp := mustRequest(t, b, s, logical.ReadOperation, "creds/writer", nil)

	name := resp.Data["name"].(string)
	if !strings.HasPrefix(name, defaultTokenPrefix+"-") {
		t.Fatalf("token name %q lacks the prefix", name)
	}
	if resp.Data["token"] != "sha-"+name || resp.Data["username"] != "bot" {
		t.Fatalf("unexpected data: %v", resp.Data)
	}
	if resp.Secret.TTL != time.Hour || resp.Secret.MaxTTL != 3*time.Hour || !resp.Secret.Renewable {
		t.Fatalf("unexpected lease: %+v", resp.Secret.LeaseOptions)
	}
	if got := f.names("bot"); len(got) != 1 || got[0] != name {
		t.Fatalf("gitea tokens = %v, want [%s]", got, name)
	}
	if entry, _ := s.Get(context.Background(), issuedPrefix+name); entry == nil {
		t.Fatal("no record of the issued token")
	}
}

func TestCredsUnknownRole(t *testing.T) {
	_, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)
	resp, err := request(b, s, logical.ReadOperation, "creds/missing", nil)
	if err != nil || !resp.IsError() {
		t.Fatalf("expected an error response, got %v, %v", resp, err)
	}
}

func TestRevokeDeletesToken(t *testing.T) {
	f, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)
	resp := mustRequest(t, b, s, logical.ReadOperation, "creds/writer", nil)
	name := resp.Data["name"].(string)

	if _, err := secretRequest(b, s, logical.RevokeOperation, resp.Secret); err != nil {
		t.Fatal(err)
	}
	if got := f.names("bot"); len(got) != 0 {
		t.Fatalf("gitea tokens after revoke = %v", got)
	}
	if entry, _ := s.Get(context.Background(), issuedPrefix+name); entry != nil {
		t.Fatal("record survived the revoke")
	}
	if _, err := secretRequest(b, s, logical.RevokeOperation, resp.Secret); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestRenewExtendsLease(t *testing.T) {
	_, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)
	resp := mustRequest(t, b, s, logical.ReadOperation, "creds/writer", nil)

	renewed, err := secretRequest(b, s, logical.RenewOperation, resp.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Secret.TTL != time.Hour || renewed.Secret.MaxTTL != 3*time.Hour {
		t.Fatalf("unexpected renewed lease: %+v", renewed.Secret.LeaseOptions)
	}

	mustRequest(t, b, s, logical.DeleteOperation, "roles/writer", nil)
	if _, err := secretRequest(b, s, logical.RenewOperation, resp.Secret); err == nil {
		t.Fatal("renew succeeded after the role was deleted")
	}
}

func TestTidyDeletesOrphans(t *testing.T) {
	f, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)
	ctx := context.Background()

	live := mustRequest(t, b, s, logical.ReadOperation, "creds/writer", nil).Data["name"].(string)
	expired := mustRequest(t, b, s, logical.ReadOperation, "creds/writer", nil).Data["name"].(string)
	entry, _ := logical.StorageEntryJSON(issuedPrefix+expired, &issuedToken{
		Username: "bot", Role: "writer", ExpireAt: time.Now().Add(-time.Minute),
	})
	if err := s.Put(ctx, entry); err != nil {
		t.Fatal(err)
	}
	f.add("bot", defaultTokenPrefix+"-orphan")
	f.add("bot", "laptop")
	f.add("bot", "other-prefix-1")

	resp := mustRequest(t, b, s, logical.UpdateOperation, "tidy", nil)

	deleted := resp.Data["deleted"].([]string)
	want := []string{defaultTokenPrefix + "-orphan", expired}
	if len(deleted) != 2 || !containsAll(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	if got := f.names("bot"); !containsAll(got, []string{live, "laptop", "other-prefix-1"}) || len(got) != 3 {
		t.Fatalf("gitea tokens after tidy = %v", got)
	}
	if entry, _ := s.Get(ctx, issuedPrefix+expired); entry != nil {
		t.Fatal("expired record survived tidy")
	}
}

func TestTidyPaginates(t *testing.T) {
	f, srv := newFakeGitea(t)
	b, s := newTestBackend(t, srv.URL)
	for i := range 120 {
		f.add("bot", defaultTokenPrefix+"-orphan"+strconv.Itoa(i))
	}
	resp := mustRequest(t, b, s, logical.UpdateOperation, "tidy", nil)
	if n := len(resp.Data["deleted"].([]string)); n != 120 {
		t.Fatalf("deleted %d tokens, want 120", n)
	}
}

func containsAll(have, want []string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
