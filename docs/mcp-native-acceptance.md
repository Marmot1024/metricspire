# Native MCP OAuth acceptance

This is maintainer release QA, not a prerequisite for each user's query. The native client owns login and credential renewal. The runner does not change configuration/scopes, read credential storage or start an inference turn.

## Run

After signing in through the existing connection, run against an explicitly reviewed staging App:

```sh
METRICSPIRE_RUN_MCP_CODEX_ACCEPTANCE=staging-read-only \
METRICSPIRE_MCP_TEST_URL='https://<reviewed-staging-app>.aws.databricksapps.com' \
METRICSPIRE_MCP_NATIVE_PHASE=first-login \
go test ./cmd/metricspire -run '^TestMCPRemoteStagingCodexNative$' -count=1 -v
```

The test checks the origin, OAuth status and seven tools on the same connection, then discovers, explains, plans, submits and polls the [TPCH fixture](../testdata/acceptance/databricks-tpch/query.json), comparing exact typed results. It repeats in a fresh process: two fixture submissions per complete run. Review the environment, account and warehouse cost first; do not use production or business data. Failures stop without alternate credentials.

Optional inputs: `METRICSPIRE_MCP_TEST_SERVER` (default `metricspire`) and `METRICSPIRE_MCP_TEST_CODEX_BIN` (default `codex`). The runner uses the [Codex app-server protocol](https://learn.chatgpt.com/docs/app-server) as verified with version 0.162.0; missing `httpOrigin` fails before submission. Other clients need their own acceptance. A test process does not establish the state of another open user window.

## Lifecycle checks

| Case | Required evidence |
| --- | --- |
| First login | Normal authentication followed by tool access and final fixture results (`first-login`). |
| New session / restart | The normal client queries without a new grant; run `new-session` as additional evidence. The runner itself also checks a fresh process. |
| Access-token expiry | After operator-verified expiry, query without a new login and run `post-expiry` with `METRICSPIRE_MCP_NATIVE_EXPIRED_AT` set to that past RFC3339 UTC time. Final results succeed without browser consent. |
| Revocation | With separate approval, revoke a disposable user's grant; confirm access fails closed and ordinary re-authentication is offered. This is manual. |
| Second user | Separately approved test accounts query under their own identities and cannot read/cancel each other's jobs. |

Record client version, environment, user alias, approved scopes, UTC times, browser prompts and returned job/release IDs; never record tokens or authorization URLs. Phase labels and supplied expiry times do not themselves prove a lifecycle event. Mark unavailable cases **not tested**; local tests do not certify deployed refresh.

`TestMCPRemoteStagingOAuthDiscovery` requires an authentication challenge by default. For reviewed Databricks Apps, `METRICSPIRE_MCP_TEST_AUTH_PROFILE=databricks_apps` allows a missing ingress challenge but still requires anonymous rejection and valid metadata; malformed challenges fail. This profile does not certify onboarding. See [MCP failure handling](mcp.md#failure-handling) for recovery.
