# Single-file Codex installer

Send the environment-specific `metricspire-install.sh` file to colleagues. They run:

```sh
sh ~/Downloads/metricspire-install.sh
```

Requires Codex CLI 0.162.0+ on PATH and macOS/Linux (arm64 or amd64). No repository, Go, Python, Databricks CLI or PAT is needed on the user's machine. The file contains a temporary setup helper, the public connection parameters and a short usage skill; it installs no query CLI or credential helper.

A new connection run from an interactive terminal opens Codex's native OAuth login once, using the provider's default scopes. Non-interactive input (including `/dev/null`) never starts login. Existing matching connections and skills stay unchanged. Conflicting parameters, disabled connections and custom skills stop with a specific message. Rerunning the installer never automatically reauthorizes an existing connection. If Codex explicitly requires login, use `codex mcp login metricspire`; the installer does not logout, clear caches or expand scopes.

Before reporting success, the installer starts a native Codex MCP verification connection, checks initialization and all seven core tools, then actually calls `list_namespaces`. A handshake, tool-loading or namespace-call failure returns a nonzero status even when OAuth authorization is saved. This check does not execute a metric query or prove post-expiry renewal.

After setup, open a fresh Codex session and ask: **使用 MetricSpire，调用 list_namespaces 验证连接。** The installed skill guides discovery and query execution. A saved OAuth record or browser callback does not prove tool access, query results or silent token renewal; validate those separately. A sandboxed diagnostic process may not share the normal client's credential access.

`--no-login` installs configuration and skill without initiating login. `--check` checks without writing files or logging in. Both perform the native connection check when saved OAuth authorization is available and return a nonzero status if authorization or that check fails. The skill is installed at `~/.agents/skills/metricspire/SKILL.md`; connection settings use `${CODEX_HOME:-~/.codex}/config.toml`. No global memory or AGENTS file is changed.

## Build and distribute

Maintainers need Go and Python 3.9+. Use the exact callback base and port already registered for this MCP URL; registration is not performed by the installer. Follow the [Codex OAuth configuration](https://learn.chatgpt.com/docs/extend/mcp) and [skill discovery](https://learn.chatgpt.com/docs/build-skills) contracts.

```sh
python3 tools/build_mcp_installer.py \
  --url 'https://<app-origin>/api/v1/mcp' \
  --client-id '<public-client-id>' \
  --callback-url 'http://127.0.0.1/callback/<registered-callback-id>' \
  --callback-port 43127
```

The builder packages four platforms and prints the whole-file SHA-256. Send that checksum alongside the file through a trusted channel; embedded payload checksums detect corruption, not a forged distributor. Generated files must stay in ignored `dist/`. Never package tokens, secrets, company semantic definitions, table mappings or business samples. The only accepted connection fields are the four public fields shown above.

The shell verifies and extracts its payload into a private temporary directory, runs the helper, then removes it. Configuration parsing preserves existing bytes and comments; new files are written through private temporary files. An installer lock and a pre-write comparison reject detected concurrent changes. Interrupted runs can leave the lock file: check that no installer is running before removing only `config.toml.metricspire-install.lock`. Do not remove OAuth caches.

Local regression tests use disposable homes and a fake Codex, including a PTY for first login. Cross-platform builds are not cross-platform runtime acceptance. First login by a second real user and expiry/renewal remain [maintainer acceptance](mcp-native-acceptance.md) cases, never implied by fixture tests.
