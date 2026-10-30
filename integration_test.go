//go:build integration

package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	adminUser     = "root"
	adminPassword = "integration-Passw0rd"
	botUser       = "bot"
)

// TestIntegration runs the engine against a real Gitea container. Run it with
// `go test -tags integration`; GITEA_IMAGE overrides the image.
func TestIntegration(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon is not reachable")
	}
	baseURL := startGitea(t)
	adminToken := createAdminToken(t, baseURL)
	giteaAPI(t, baseURL, adminToken, http.MethodPost, "/admin/users", map[string]any{
		"username": botUser, "email": "bot@example.com", "password": adminPassword, "must_change_password": false,
	}, http.StatusCreated, nil)

	cfg := logical.TestBackendConfig()
	cfg.StorageView = &logical.InmemStorage{}
	cfg.System = &logical.StaticSystemView{DefaultLeaseTTLVal: time.Hour, MaxLeaseTTLVal: 24 * time.Hour}
	b := newBackend()
	if err := b.Setup(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.StorageView
	mustRequest(t, b, s, logical.UpdateOperation, "config", map[string]any{"base_url": baseURL, "token": adminToken})
	mustRequest(t, b, s, logical.UpdateOperation, "roles/writer", map[string]any{
		"username": botUser, "scopes": "write:repository,read:user", "ttl": "1h", "max_ttl": "2h",
	})

	resp := mustRequest(t, b, s, logical.ReadOperation, "creds/writer", nil)
	issued := resp.Data["token"].(string)
	var me struct {
		Login string `json:"login"`
	}
	giteaAPI(t, baseURL, issued, http.MethodGet, "/user", nil, http.StatusOK, &me)
	if me.Login != botUser {
		t.Fatalf("issued token authenticates as %q, want %q", me.Login, botUser)
	}

	if _, err := secretRequest(b, s, logical.RevokeOperation, resp.Secret); err != nil {
		t.Fatal(err)
	}
	giteaAPI(t, baseURL, issued, http.MethodGet, "/user", nil, http.StatusUnauthorized, nil)

	c, _, err := b.getClient(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.createToken(context.Background(), botUser, defaultTokenPrefix+"-orphan", []string{"read:user"}); err != nil {
		t.Fatal(err)
	}
	tidy := mustRequest(t, b, s, logical.UpdateOperation, "tidy", nil)
	if deleted := tidy.Data["deleted"].([]string); len(deleted) != 1 || deleted[0] != defaultTokenPrefix+"-orphan" {
		t.Fatalf("tidy deleted %v", deleted)
	}
	left, err := c.listTokens(context.Background(), botUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("tokens left after tidy: %v", left)
	}
}

func startGitea(t *testing.T) string {
	t.Helper()
	image := os.Getenv("GITEA_IMAGE")
	if image == "" {
		image = "gitea/gitea:1.24"
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::3000",
		"-e", "GITEA__security__INSTALL_LOCK=true",
		"-e", "GITEA__database__DB_TYPE=sqlite3",
		image).Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	out, err = exec.Command("docker", "port", id, "3000/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	baseURL := "http://" + strings.TrimSpace(strings.Split(string(out), "\n")[0])

	deadline := time.Now().Add(2 * time.Minute)
	for {
		resp, err := http.Get(baseURL + "/api/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gitea at %s is not healthy: %v", baseURL, err)
		}
		time.Sleep(time.Second)
	}

	create := exec.Command("docker", "exec", "-u", "git", id, "gitea", "admin", "user", "create",
		"--admin", "--username", adminUser, "--password", adminPassword,
		"--email", "root@example.com", "--must-change-password=false")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create the admin user: %v: %s", err, out)
	}
	return baseURL
}

func createAdminToken(t *testing.T, baseURL string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name":   "openbao",
		"scopes": []string{"write:admin", "write:user"},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/users/"+adminUser+"/tokens", bytes.NewReader(body))
	req.SetBasicAuth(adminUser, adminPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok accessToken
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create the admin token: status %d: %v", resp.StatusCode, err)
	}
	return tok.SHA1
}

func giteaAPI(t *testing.T, baseURL, token, method, path string, in any, wantStatus int, out any) {
	t.Helper()
	var body *bytes.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, baseURL+"/api/v1"+path, body)
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d", method, path, resp.StatusCode, wantStatus)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(fmt.Errorf("decode %s: %w", path, err))
		}
	}
}
