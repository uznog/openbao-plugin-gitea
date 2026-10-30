package gitea

import (
	"context"
	"time"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const rolePrefix = "roles/"

type role struct {
	Username string        `json:"username"`
	Scopes   []string      `json:"scopes"`
	TTL      time.Duration `json:"ttl"`
	MaxTTL   time.Duration `json:"max_ttl"`
}

func pathRoles(b *backend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "roles/?$",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{Callback: b.pathRoleList},
			},
			HelpSynopsis: "List the configured roles.",
		},
		{
			Pattern: "roles/" + framework.GenericNameRegex("name"),
			Fields: map[string]*framework.FieldSchema{
				"name": {
					Type:        framework.TypeLowerCaseString,
					Description: "Name of the role.",
					Required:    true,
				},
				"username": {
					Type:        framework.TypeString,
					Description: "Gitea user that owns the issued tokens.",
				},
				"scopes": {
					Type:        framework.TypeCommaStringSlice,
					Description: "Gitea token scopes, for example write:repository,read:user.",
				},
				"ttl": {
					Type:        framework.TypeDurationSecond,
					Description: "Default lease TTL. Zero uses the mount default.",
				},
				"max_ttl": {
					Type:        framework.TypeDurationSecond,
					Description: "Maximum lease TTL after renewals. Zero uses the mount maximum.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation:   &framework.PathOperation{Callback: b.pathRoleRead},
				logical.CreateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
				logical.UpdateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
				logical.DeleteOperation: &framework.PathOperation{Callback: b.pathRoleDelete},
			},
			ExistenceCheck:  b.pathRoleExists,
			HelpSynopsis:    "Manage the roles that map to Gitea users and token scopes.",
			HelpDescription: "A role names one Gitea user, the scopes of its tokens, and the lease TTLs.",
		},
	}
}

func getRole(ctx context.Context, s logical.Storage, name string) (*role, error) {
	entry, err := s.Get(ctx, rolePrefix+name)
	if err != nil || entry == nil {
		return nil, err
	}
	var r role
	if err := entry.DecodeJSON(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (b *backend) pathRoleExists(ctx context.Context, req *logical.Request, d *framework.FieldData) (bool, error) {
	r, err := getRole(ctx, req.Storage, d.Get("name").(string))
	return r != nil, err
}

func (b *backend) pathRoleList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	names, err := req.Storage.List(ctx, rolePrefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(names), nil
}

func (b *backend) pathRoleRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	r, err := getRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil || r == nil {
		return nil, err
	}
	return &logical.Response{Data: map[string]any{
		"username": r.Username,
		"scopes":   r.Scopes,
		"ttl":      int64(r.TTL.Seconds()),
		"max_ttl":  int64(r.MaxTTL.Seconds()),
	}}, nil
}

func (b *backend) pathRoleWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r = &role{}
	}
	if v, ok := d.GetOk("username"); ok {
		r.Username = v.(string)
	}
	if v, ok := d.GetOk("scopes"); ok {
		r.Scopes = v.([]string)
	}
	if v, ok := d.GetOk("ttl"); ok {
		r.TTL = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("max_ttl"); ok {
		r.MaxTTL = time.Duration(v.(int)) * time.Second
	}

	if r.Username == "" {
		return logical.ErrorResponse("username is required"), nil
	}
	if len(r.Scopes) == 0 {
		return logical.ErrorResponse("scopes must name at least one Gitea scope"), nil
	}
	if r.MaxTTL > 0 && r.TTL > r.MaxTTL {
		return logical.ErrorResponse("ttl must not be greater than max_ttl"), nil
	}

	entry, err := logical.StorageEntryJSON(rolePrefix+name, r)
	if err != nil {
		return nil, err
	}
	return nil, req.Storage.Put(ctx, entry)
}

func (b *backend) pathRoleDelete(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	return nil, req.Storage.Delete(ctx, rolePrefix+d.Get("name").(string))
}
