package gitea

import (
	"context"
	"errors"
	"net/url"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	pathConfig         = "config"
	defaultTokenPrefix = "openbao"
)

var errNotConfigured = errors.New("the Gitea backend is not configured; write to config first")

type giteaConfig struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token"`
	CACert      string `json:"ca_cert"`
	TokenPrefix string `json:"token_prefix"`
}

func pathConfigure(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: pathConfig,
		Fields: map[string]*framework.FieldSchema{
			"base_url": {
				Type:        framework.TypeString,
				Description: "Base URL of the Gitea server, for example https://gitea.example.com.",
			},
			"token": {
				Type:        framework.TypeString,
				Description: "Access token of a Gitea site administrator. The engine never returns it.",
				DisplayAttrs: &framework.DisplayAttributes{
					Sensitive: true,
				},
			},
			"ca_cert": {
				Type:        framework.TypeString,
				Description: "PEM CA bundle that verifies the Gitea TLS certificate. Empty uses the system roots.",
			},
			"token_prefix": {
				Type:        framework.TypeString,
				Description: "Name prefix of issued tokens. Tidy deletes unknown tokens with this prefix, so give each mount on one Gitea a unique prefix.",
				Default:     defaultTokenPrefix,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation:   &framework.PathOperation{Callback: b.pathConfigRead},
			logical.CreateOperation: &framework.PathOperation{Callback: b.pathConfigWrite},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathConfigWrite},
			logical.DeleteOperation: &framework.PathOperation{Callback: b.pathConfigDelete},
		},
		ExistenceCheck:  b.pathConfigExists,
		HelpSynopsis:    "Configure the Gitea server and the admin token.",
		HelpDescription: "The admin token creates and deletes tokens for other users through the Sudo header.",
	}
}

func getConfig(ctx context.Context, s logical.Storage) (*giteaConfig, error) {
	entry, err := s.Get(ctx, pathConfig)
	if err != nil || entry == nil {
		return nil, err
	}
	var cfg giteaConfig
	if err := entry.DecodeJSON(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (b *backend) pathConfigExists(ctx context.Context, req *logical.Request, _ *framework.FieldData) (bool, error) {
	cfg, err := getConfig(ctx, req.Storage)
	return cfg != nil, err
}

func (b *backend) pathConfigRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil || cfg == nil {
		return nil, err
	}
	return &logical.Response{Data: map[string]any{
		"base_url":     cfg.BaseURL,
		"ca_cert":      cfg.CACert,
		"token_prefix": cfg.TokenPrefix,
	}}, nil
}

func (b *backend) pathConfigWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &giteaConfig{TokenPrefix: defaultTokenPrefix}
	}
	if v, ok := d.GetOk("base_url"); ok {
		cfg.BaseURL = v.(string)
	}
	if v, ok := d.GetOk("token"); ok {
		cfg.Token = v.(string)
	}
	if v, ok := d.GetOk("ca_cert"); ok {
		cfg.CACert = v.(string)
	}
	if v, ok := d.GetOk("token_prefix"); ok {
		cfg.TokenPrefix = v.(string)
	}

	if u, err := url.Parse(cfg.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return logical.ErrorResponse("base_url must be an absolute URL"), nil
	}
	if cfg.Token == "" {
		return logical.ErrorResponse("token is required"), nil
	}
	if cfg.TokenPrefix == "" {
		return logical.ErrorResponse("token_prefix must not be empty"), nil
	}
	if _, err := newClient(cfg); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	entry, err := logical.StorageEntryJSON(pathConfig, cfg)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}
	b.reset()
	return nil, nil
}

func (b *backend) pathConfigDelete(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	if err := req.Storage.Delete(ctx, pathConfig); err != nil {
		return nil, err
	}
	b.reset()
	return nil, nil
}
