// Package gitea implements an OpenBao secrets engine that issues Gitea access
// tokens bound to a lease.
package gitea

import (
	"context"
	"strings"
	"sync"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const backendHelp = `
The Gitea secrets engine issues Gitea personal access tokens for configured
users. Each token belongs to a lease, and the engine deletes the token in Gitea
when the lease is revoked or expires.
`

type backend struct {
	*framework.Backend

	lock   sync.RWMutex
	client *client
}

// Factory returns a configured instance of the backend.
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := newBackend()
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

func newBackend() *backend {
	b := &backend{}
	b.Backend = &framework.Backend{
		Help:        strings.TrimSpace(backendHelp),
		BackendType: logical.TypeLogical,
		PathsSpecial: &logical.Paths{
			SealWrapStorage: []string{pathConfig},
		},
		Paths: framework.PathAppend(
			[]*framework.Path{pathConfigure(b), pathTidy(b)},
			pathRoles(b),
			[]*framework.Path{pathCreds(b)},
		),
		Secrets:    []*framework.Secret{secretToken(b)},
		Invalidate: b.invalidate,
	}
	return b
}

func (b *backend) invalidate(_ context.Context, key string) {
	if key == pathConfig {
		b.reset()
	}
}

func (b *backend) reset() {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.client = nil
}

// getClient returns the cached Gitea client and builds it from the stored
// config on first use.
func (b *backend) getClient(ctx context.Context, s logical.Storage) (*client, *giteaConfig, error) {
	cfg, err := getConfig(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, errNotConfigured
	}

	b.lock.RLock()
	c := b.client
	b.lock.RUnlock()
	if c != nil {
		return c, cfg, nil
	}

	b.lock.Lock()
	defer b.lock.Unlock()
	if b.client == nil {
		if b.client, err = newClient(cfg); err != nil {
			return nil, nil, err
		}
	}
	return b.client, cfg, nil
}
