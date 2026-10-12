# Codex MCP release acceptance

The current local Codex route uses the installed `metricspire-cli` connection and an existing Databricks CLI U2M profile. Authentication is supplied by CLI; all analytical calls use native Codex MCP. The runner neither starts login nor reads credential storage.

## Deployment gate

`tools/deploy_staging.py` packages an explicit reviewed commit, preserves the active deployment's frozen configuration and runs `tools/release-gate` before and after deployment. Required identity inputs include `--profile` for deployment management and `--auth-profile` for the installed MCP user profile. Use `--help` for target and fixture arguments.

The gate checks:

1. App scopes match OAuth resource/issuer metadata.
2. The installed helper matches the target workspace/profile, and Codex initializes the expected MCP tools.
3. The serving MCP version matches the full target commit (post-deployment).
4. A bounded fixture is planned, submitted once and polled; its complete typed result matches expectations. Configured exploration requires its own fixture.

Use the neutral [TPCH](../testdata/acceptance/databricks-tpch) and [calendar](../testdata/acceptance/exploration-calendar) fixtures. Failed preflight stops deployment. Failed post-deployment acceptance restores the prior frozen package only while this release remains active, then verifies the restored query path. Real failure-triggered rollback still needs acceptance.

## Authentication lifecycle

Current tool loading or successful queries do not prove renewal. Verify a new process, then keep a native connection across an observed access-token expiry and query without login. Separately exercise a same-origin rejected-token response and confirm helper refresh/retry. Record only client versions, UTC times, non-secret expiry metadata, returned job IDs and outcomes; never record tokens or authorization URLs.

Second-user onboarding, permission isolation, revoked-grant recovery and other platforms require separate acceptance. Do not revoke shared grants for a test. The legacy `TestMCPRemoteStagingCodexNative` runner specifically checks native OAuth; its phase labels are not evidence for the CLI-helper route. The preserved OAuth development branch retains that setup.
