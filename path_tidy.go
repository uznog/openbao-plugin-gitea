package gitea

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathTidy(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "tidy$",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathTidyWrite},
		},
		HelpSynopsis: "Delete orphaned Gitea tokens.",
		HelpDescription: "Tidy deletes each Gitea token with the configured prefix that has no record, " +
			"and each recorded token past its maximum lease TTL. It scans the users of all roles " +
			"and of all records.",
	}
}

func (b *backend) pathTidyWrite(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	c, cfg, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}

	users := map[string]struct{}{}
	roleNames, err := req.Storage.List(ctx, rolePrefix)
	if err != nil {
		return nil, err
	}
	for _, name := range roleNames {
		r, err := getRole(ctx, req.Storage, name)
		if err != nil {
			return nil, err
		}
		if r != nil {
			users[r.Username] = struct{}{}
		}
	}
	records, err := loadIssued(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		users[rec.Username] = struct{}{}
	}

	// Gitea is listed before the records are read again, so a token that
	// creds issues during the scan already has its record.
	remote := map[string][]string{}
	for user := range users {
		toks, err := c.listTokens(ctx, user)
		if err != nil {
			return nil, err
		}
		for _, t := range toks {
			if strings.HasPrefix(t.Name, cfg.TokenPrefix+"-") {
				remote[user] = append(remote[user], t.Name)
			}
		}
	}
	if records, err = loadIssued(ctx, req.Storage); err != nil {
		return nil, err
	}

	deleted := []string{}
	for user, names := range remote {
		for _, name := range names {
			if rec, ok := records[name]; ok && rec.Username == user {
				continue
			}
			if err := c.deleteToken(ctx, user, name); err != nil {
				return nil, err
			}
			deleted = append(deleted, name)
		}
	}
	now := time.Now()
	for name, rec := range records {
		if now.Before(rec.ExpireAt) {
			continue
		}
		if err := c.deleteToken(ctx, rec.Username, name); err != nil {
			return nil, err
		}
		if err := req.Storage.Delete(ctx, issuedPrefix+name); err != nil {
			return nil, err
		}
		deleted = append(deleted, name)
	}

	sort.Strings(deleted)
	return &logical.Response{Data: map[string]any{"deleted": deleted}}, nil
}

func loadIssued(ctx context.Context, s logical.Storage) (map[string]*issuedToken, error) {
	names, err := s.List(ctx, issuedPrefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*issuedToken, len(names))
	for _, name := range names {
		entry, err := s.Get(ctx, issuedPrefix+name)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			continue
		}
		var rec issuedToken
		if err := entry.DecodeJSON(&rec); err != nil {
			return nil, err
		}
		out[name] = &rec
	}
	return out, nil
}
