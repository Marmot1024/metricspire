# Codex CLI installer

Use local **Codex CLI 0.162.0+** and an existing **Databricks CLI U2M profile** for the App workspace. The user still needs App, warehouse and data permissions. No repository checkout, Go, Python, PAT or custom OAuth client is needed on the user's machine.

## Get the installer

Ask your team's MetricSpire maintainer for `metricspire-install.sh`, the expected workspace and a SHA-256 checksum supplied through a trusted team channel. The maintainer builds it for your deployed instance using the [build steps below](#build). There is no universal public installer download: each file contains that instance's connection addresses. You do not need to build the service yourself.

Save the file in `~/Downloads` and verify its SHA-256 against the maintainer's value (`shasum -a 256` on macOS or `sha256sum` on Linux). Use your existing CLI profile for that workspace.

## Install and verify

```sh
sh ~/Downloads/metricspire-install.sh --profile '<existing-profile>'
```

The installer verifies the profile's workspace and user-authentication type, installs a content-addressed header helper and a short usage skill, configures `metricspire-cli`, then verifies native MCP initialization, the seven legacy query tools required as a compatibility baseline and an actual `list_namespaces` call. Saved authorization alone is not success. Codex may label helper authentication `unknown`; actual tool loading and calls determine connectivity. Open a fresh Codex session after installation. Current hosted servers can advertise eleven core tools plus five optional exploration tools; passing the installer check does not verify every tool or a warehouse query.

In the new session, ask the assistant to list your available business domains, then find a metric and inspect its definition. Once its meaning and your query scope are clear, request a bounded query and wait for a successful result. See the [query workflow](mcp.md#query-workflow).

Databricks CLI owns OAuth storage, token acquisition and refresh. The helper only returns an in-memory Authorization header to Codex. It never logs in, stores tokens or queries data. Codex caches headers per connection and can refresh them after a same-origin POST receives 401/403; this does not make expired or revoked refresh grants permanent. See [Codex HTTP helpers](https://learn.chatgpt.com/docs/extend/mcp?surface=cli) and [Databricks U2M](https://docs.databricks.com/aws/en/dev-tools/auth/oauth-u2m).

- `--check`: verify without changing installation files; normal CLI token refresh may still occur.
- `--migrate-oauth`: back up and disable a matching old `metricspire` OAuth connection; preserve its configuration and credential cache. Upgrade only the recognized previous distributed skill. A separate connection name prevents old OAuth credentials from shadowing helper headers.
- `--upgrade`: back up and replace an intact managed helper for the same workspace/profile; other settings stay unchanged.
- Conflicting URLs, custom skills, mixed authentication or changed helper binaries stop without replacement. No automatic login, cache deletion or scope changes occur. If CLI authorization has actually expired, the user logs in to the same workspace/profile with `databricks auth login`.

Configuration and helpers live under `${CODEX_HOME:-~/.codex}`; the skill is `~/.agents/skills/metricspire/SKILL.md`. Config backups are private adjacent files. Existing comments and unrelated settings are preserved. These helpers require a local Codex process, not a remote execution environment.

## Build

Maintainers need Go and Python 3.9+:

```sh
python3 tools/build_mcp_installer.py \
  --url 'https://<app-origin>/api/v1/mcp' \
  --workspace-host 'https://<workspace-host>'
```

The file supports macOS/Linux arm64/amd64. Distribute its printed SHA-256 through a trusted channel. Only the two public addresses are packaged; keep generated `dist/`, credentials and company semantic assets out of Git. Cross-builds and fixture tests do not establish second-user or cross-platform runtime acceptance.

The shell checks payload integrity and removes extraction files. Installation uses a lock and compares files before replacement. If interrupted, confirm no installer is running before removing only `config.toml.metricspire-install.lock`. Restore an adjacent config backup to return to the preserved OAuth setup; do not delete credential caches. Native OAuth code is retained on `backup/native-oauth-20261012`.
