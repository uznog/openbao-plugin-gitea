package gitea

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	secretTokenType = "gitea_token"
	issuedPrefix    = "tokens/"
)

// issuedToken is the storage record of one token that a lease owns. Tidy treats
// a prefixed Gitea token without a record as an orphan.
type issuedToken struct {
	Username string    `json:"username"`
	Role     string    `json:"role"`
	ExpireAt time.Time `json:"expire_at"`
}

func pathCreds(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "creds/" + framework.GenericNameRegex("role"),
		Fields: map[string]*framework.FieldSchema{
			"role": {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role.",
				Required:    true,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{Callback: b.pathCredsRead},
		},
		HelpSynopsis:    "Issue a Gitea access token for a role.",
		HelpDescription: "The token belongs to a lease. Revocation of the lease deletes the token in Gitea.",
	}
}

func secretToken(b *backend) *framework.Secret {
	return &framework.Secret{
		Type: secretTokenType,
		Fields: map[string]*framework.FieldSchema{
			"token": {Type: framework.TypeString, Description: "Gitea access token."},
			"name":  {Type: framework.TypeString, Description: "Name of the token in Gitea."},
		},
		Renew:  b.secretTokenRenew,
		Revoke: b.secretTokenRevoke,
	}
}

func (b *backend) pathCredsRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	roleName := d.Get("role").(string)
	r, err := getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return logical.ErrorResponse("role %q does not exist", roleName), nil
	}
	c, cfg, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	name := cfg.TokenPrefix + "-" + hex.EncodeToString(suffix)

	maxTTL := b.System().MaxLeaseTTL()
	if r.MaxTTL > 0 && r.MaxTTL < maxTTL {
		maxTTL = r.MaxTTL
	}
	// The record is written before the token exists, so a concurrent tidy
	// never sees the new token without its record.
	entry, err := logical.StorageEntryJSON(issuedPrefix+name, &issuedToken{
		Username: r.Username,
		Role:     roleName,
		ExpireAt: time.Now().Add(maxTTL),
	})
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}

	tok, err := c.createToken(ctx, r.Username, name, r.Scopes)
	if err != nil {
		if delErr := req.Storage.Delete(ctx, issuedPrefix+name); delErr != nil {
			b.Logger().Warn("delete the record of a failed token", "name", name, "error", delErr)
		}
		return nil, fmt.Errorf("create the Gitea token: %w", err)
	}

	resp := b.Secret(secretTokenType).Response(map[string]any{
		"token":    tok.SHA1,
		"name":     name,
		"username": r.Username,
		"scopes":   r.Scopes,
	}, map[string]any{
		"name":     name,
		"username": r.Username,
		"role":     roleName,
	})
	resp.Secret.TTL = r.TTL
	resp.Secret.MaxTTL = r.MaxTTL
	return resp, nil
}

func (b *backend) secretTokenRenew(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	roleName, _ := req.Secret.InternalData["role"].(string)
	r, err := getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("role %q no longer exists", roleName)
	}
	resp := &logical.Response{Secret: req.Secret}
	resp.Secret.TTL = r.TTL
	resp.Secret.MaxTTL = r.MaxTTL
	return resp, nil
}

func (b *backend) secretTokenRevoke(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	name, _ := req.Secret.InternalData["name"].(string)
	username, _ := req.Secret.InternalData["username"].(string)
	if name == "" || username == "" {
		return nil, fmt.Errorf("the lease holds no token name or username")
	}
	c, _, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if err := c.deleteToken(ctx, username, name); err != nil {
		return nil, fmt.Errorf("delete the Gitea token: %w", err)
	}
	return nil, req.Storage.Delete(ctx, issuedPrefix+name)
}
