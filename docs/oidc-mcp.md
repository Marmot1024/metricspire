# Standard OIDC MCP onboarding

A self-hosted team can use PostgreSQL/ClickHouse serving with an enterprise OIDC
issuer and native MCP OAuth, without Databricks accounts or CLI profiles. Use the
independent connection **`metricspire-oidc`**; keep existing Databricks helper
connections unchanged. This path uses a registered **public** OAuth client, PKCE
S256 and client-owned credential storage/refresh. No client secret or shared PAT
is distributed.

## Operator configuration

1. Deploy MetricSpire with `authentication.provider: oidc`, the reviewed issuer,
   bearer audience and tenant/role/permission claim mappings. See
   [runtime configuration](runtime-engines.md) and the [HTTP guide](http-api.md).
   Access tokens must be JWTs accepted by that audience verifier; an ID token or
   a successful login alone does not grant metric access.
2. Register a separate public native MCP client at your issuer. Enable the
   authorization-code flow with S256 PKCE and refresh grants (rotation/revocation
   according to your provider). Map the intended API audience and claims into
   access tokens. Register the exact loopback callback displayed by Codex. The
   service's web `/auth/callback` client is a different client and redirect.
3. Use Codex CLI 0.162.0+ and the provider's approved scopes, including its refresh
   scope if required. Providers must expose OIDC metadata plus OAuth metadata
   usable by the MCP client. Allow variable loopback ports as required by
   [RFC 8252](https://www.rfc-editor.org/rfc/rfc8252#section-7.3).
   Issuer-bound responses use the configured stable callback; other providers
   require a server-specific callback. Follow the current
   [official Codex OAuth instructions](https://learn.chatgpt.com/docs/extend/mcp?surface=cli#oauth-client-registration-and-callbacks).
4. Build the team's single-file installer using Go and Python 3.9+:

   ```sh
   python3 tools/build_mcp_installer.py \
     --url 'https://metrics.example.com/api/v1/mcp' \
     --oidc-issuer 'https://identity.example.com/realms/team' \
     --oauth-client-id 'registered-public-mcp-client' \
     --scope metrics --scope offline_access \
     --output dist/metricspire-oidc-install.sh
   ```

The addresses and scopes above are placeholders. Distribute the file and its
printed SHA-256 through a trusted team channel. The package contains public
connection metadata and a native installer binary for macOS/Linux arm64/amd64;
end users need Codex, not Go, Python or a checkout. Artifacts remain in ignored
`dist/`. Private semantic catalogs, credentials and business rows are excluded.

## User installation

Verify the supplied SHA-256, then run:

```sh
sh ~/Downloads/metricspire-oidc-install.sh
```

The installer checks the MCP resource and issuer against the reviewed bundle,
requires advertised S256 support, preserves unrelated Codex configuration and
creates a private backup before adding the connection. It refuses a conflicting
existing connection or mixed authentication rather than replacing it. Native
Codex login performs authentication; the installer never reads the token cache.
The exact callback must already be registered by the operator. Browser-free
login is available through `codex mcp login metricspire-oidc --no-browser`.

- `--configure-only` installs the public connection and prints the explicit login
  command with the reviewed scopes; it does not claim a successful connection.
- `--check` changes no configuration and performs no login. It checks effective
  endpoint/client/auth configuration, native MCP initialization, the seven core
  compatibility tools and an authorized `list_namespaces` call. Normal native
  token refresh may occur. It does not certify a business-data query.
- Configuration lives under `${CODEX_HOME:-~/.codex}`; backups use the private
  adjacent `config.toml.metricspire-oidc.backup` file. Installation uses an
  exclusive lock; inspect any interrupted installer before removing its lock.
  Restore the backup to undo configuration; preserve credential caches.

Open a fresh client session, discover query routes, compare the selected metric
scope and request a bounded result. On `online_metrics`, use
[the online MCP workflow](mcp.md#query-workflow). A revoked refresh grant requires
user reauthentication; repeated login does not fix policy denial or unavailable
identity infrastructure. Do not paste tokens, callback codes or private schemas
into conversations or issue reports.

## Acceptance boundary

The opt-in `tools/test_oidc_mcp.py` uses real local Codex with two isolated client
homes and a synthetic loopback issuer/MCP server. It checks code exchange with
S256, expiry refresh, refresh-token rotation across client restarts, revocation
and independent identities. The full service database tests separately check
OIDC JWT validation and online reader-policy denial. These are distinct checks;
they do not certify an enterprise provider, a human colleague's first login,
cross-platform runtime or long-running production service.
