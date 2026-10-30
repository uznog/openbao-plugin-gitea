# openbao-plugin-gitea

An [OpenBao](https://openbao.org) secrets engine that issues short-lived
[Gitea](https://about.gitea.com) access tokens.

Gitea personal access tokens never expire and cannot be limited to selected
repositories. This engine binds each token to an OpenBao lease: the engine
creates the token when a client reads `creds/<role>`, and deletes it in Gitea
when the lease is revoked or expires.

The engine builds against the OpenBao SDK (`github.com/openbao/openbao/sdk/v2`).

## Install

Build the plugin and copy it into the OpenBao plugin directory:

```sh
go build -o openbao-plugin-gitea ./cmd/openbao-plugin-gitea
cp openbao-plugin-gitea /etc/openbao/plugins/
```

Register the plugin with its SHA-256 sum, then mount it:

```sh
bao plugin register \
  -sha256="$(sha256sum /etc/openbao/plugins/openbao-plugin-gitea | cut -d' ' -f1)" \
  secret openbao-plugin-gitea

bao secrets enable -path=gitea openbao-plugin-gitea
```

## Configure

The engine needs the access token of a Gitea site administrator. It uses that
token with the `Sudo` header to create and delete tokens for other users.
Give the admin token the `write:admin` and `write:user` scopes.

```sh
bao write gitea/config \
  base_url=https://gitea.example.com \
  token=@admin-token.txt \
  ca_cert=@ca.pem \
  token_prefix=openbao
```

| Field          | Description                                                                 |
|----------------|-----------------------------------------------------------------------------|
| `base_url`     | Base URL of the Gitea server. Required. It cannot change while issued tokens exist. |
| `token`        | Admin access token. Required. A read of `config` never returns it.          |
| `ca_cert`      | PEM CA bundle that verifies the Gitea TLS certificate. Empty uses the system roots. |
| `token_prefix` | Name prefix of issued tokens. Default `openbao`.                            |

Give each mount on one Gitea server a unique `token_prefix`. Tidy deletes every
token with the prefix that the mount did not issue.

## Roles

A role maps to one Gitea user and one set of token scopes:

```sh
bao write gitea/roles/ci \
  username=ci-bot \
  scopes=write:repository,read:user \
  ttl=1h \
  max_ttl=8h
```

| Field      | Description                                                        |
|------------|--------------------------------------------------------------------|
| `username` | Gitea user that owns the issued tokens. Required.                  |
| `scopes`   | Gitea token scopes, comma-separated. Required.                     |
| `ttl`      | Default lease TTL. Zero uses the mount default.                    |
| `max_ttl`  | Maximum lease TTL after renewals. Zero uses the mount maximum.     |

`bao list gitea/roles` lists the roles.

## Usage

Read a token:

```sh
$ bao read gitea/creds/ci
Key                Value
---                -----
lease_id           gitea/creds/ci/kBx8...
lease_duration     1h
lease_renewable    true
name               openbao-3f9c2a1b7d4e6f80
scopes             [write:repository read:user]
token              4a1f...
username           ci-bot
```

Renew the lease up to `max_ttl`, or revoke it to delete the token in Gitea:

```sh
bao lease renew gitea/creds/ci/kBx8...
bao lease revoke gitea/creds/ci/kBx8...
```

Delete orphaned tokens:

```sh
bao write -f gitea/tidy
```

Tidy scans the users of all roles and of all issued tokens. It deletes each
token whose name has the mount prefix but which the mount has no record of. It
also deletes each recorded token that is past its maximum lease TTL. Run tidy
on a schedule, for example after an OpenBao restore or a failed revocation.

## Security model

- The admin token is a Gitea site administrator credential. The engine stores
  it in seal-wrapped storage and never returns it.
- An issued token has the scopes of its role and all the access of the role's
  user. Gitea cannot limit a token to selected repositories, so limit the
  user's repository access in Gitea.
- Gitea tokens have no expiry. A token stays valid until OpenBao revokes its
  lease. Revocation deletes the token in Gitea; tidy removes tokens that a
  failed revocation left behind.
- Each issued token has a random name, `<token_prefix>-<16 hex characters>`.
  Gitea token names are unique per user, so a name never collides with a
  token that the user made by hand unless it uses the same prefix.
- Policies on `gitea/creds/<role>` decide which OpenBao clients can get a token
  for which Gitea user.

## Development

```sh
go vet ./...
go test ./...
go test -tags integration -run TestIntegration -v ./...
```

The integration test starts a Gitea container with Docker. It skips when
Docker is absent. Set `GITEA_IMAGE` to test another Gitea version.
