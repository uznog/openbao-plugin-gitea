package gitea

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client calls the Gitea token API as an admin that impersonates the target
// user through the Sudo header.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

type accessToken struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	SHA1   string   `json:"sha1,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

func newClient(cfg *giteaConfig) (*client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("ca_cert holds no valid PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		token:   cfg.Token,
		http:    &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}, nil
}

func (c *client) createToken(ctx context.Context, username, name string, scopes []string) (*accessToken, error) {
	body := map[string]any{"name": name, "scopes": scopes}
	var tok accessToken
	if err := c.do(ctx, http.MethodPost, username, "/tokens", body, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func (c *client) listTokens(ctx context.Context, username string) ([]accessToken, error) {
	const limit = 50
	var all []accessToken
	for page := 1; ; page++ {
		var batch []accessToken
		path := fmt.Sprintf("/tokens?page=%d&limit=%d", page, limit)
		if err := c.do(ctx, http.MethodGet, username, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < limit {
			return all, nil
		}
	}
}

// deleteToken removes a token by name. A token that no longer exists counts as
// deleted, so a repeated revoke succeeds.
func (c *client) deleteToken(ctx context.Context, username, name string) error {
	err := c.do(ctx, http.MethodDelete, username, "/tokens/"+url.PathEscape(name), nil, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
		return nil
	}
	return err
}

type apiError struct {
	method, path string
	status       int
	body         string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("gitea %s %s: status %d: %s", e.method, e.path, e.status, e.body)
}

func (c *client) do(ctx context.Context, method, username, path string, in, out any) error {
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	fullPath := "/api/v1/users/" + url.PathEscape(username) + path
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+fullPath, reqBody)
	if err != nil {
		return err
	}
	// Gitea serves the token endpoints only to basic auth. A token in the
	// username field with an empty password authenticates as that token.
	req.SetBasicAuth(c.token, "")
	req.Header.Set("Sudo", username)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &apiError{method: method, path: fullPath, status: resp.StatusCode, body: strings.TrimSpace(string(msg))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
