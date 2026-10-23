# OpenCode acceptance

This maintainer workflow tests a real model/client against an isolated MetricSpire
service containing approved neutral fixtures. It does not install a team service
or establish business certification. Ordinary users of an existing service start
with [connection setup](mcp.md#connect), then ask their question; they do not need
to run this test harness or know the repository.

## Prepare once

Use an existing OpenCode V2 installation, model authorization and dedicated
loopback OpenCode server. Keep the test project outside company repositories and
configure only the isolated MetricSpire MCP. Do not expose a shared company catalog
to an external model. Reuse authentication without login, cache resets or copied
tokens. A tunnel to a remote test client must bind both ends to loopback.

The project uses the default agent, with no custom `system`, `instructions`,
skills, handoff or previous conversation. Restrict its permissions to the test
MCP tools and expose them directly (`codemode: false`). This isolates the test
from shell/file/web tools while leaving the model's query decisions observable.
OpenCode V2 uses `mcp.servers` and `permissions`; follow the
[official MCP configuration](https://dev.opencode.ai/v2/docs/mcp-servers) for the
installed version. The Codex-specific `http_headers_helper` is not an OpenCode
configuration option; use an already authorized connection supported by that
client. Keep its credentials outside configuration files and receipts.

Record the service commit/diff, OpenCode version, selected model, fixture contents,
expected results and permissions. Real HTTP/MCP/planning with a mock engine is
still a mock execution test. Identify it explicitly.

## Run each user turn

From the machine running OpenCode, use Python 3 and the existing server:

```sh
python3 tools/run_opencode_mcp.py \
  --directory /path/to/isolated-project \
  --server http://127.0.0.1:49327 \
  --model '<provider/model>' \
  --output /path/to/private/new-receipt.json \
  'First connection: discover the domains, then query the requested metric and period.'
```

Use the actual user question unchanged. The runner synchronizes `PWD` with the
project, waits at most 30 seconds for that project's MCP connection, then sends
one model turn. It injects no agent/system prompt and does not retry model calls.
Continue clarification with `--session <returned-session-id>`. Receipts must use
new private filenames; existing failure evidence is never overwritten. On a
timeout, inspect the existing session and accepted job before retrying.

The runner checks real completed MCP events as well as CLI status. Tool-shaped
text or exit zero alone fails this interaction check. Its success means only that
an actual tool interaction completed; inspect the answer and server trace for
semantic acceptance. Readiness is client initialization time, reported separately
from the model turn. A readiness failure stops before invoking the model and
does not trigger login. Raw CLI diagnostics are suppressed to avoid credential
exposure; retain authorized local diagnostics separately if needed.

## Review behavior

- Ask an ambiguous same-family metric question in fresh sessions. The assistant
  must compare definitions, present unresolved scope choices and stop without
  execution. “Total” does not choose the broadest population. Repeat the exact
  question; retain every failure, then give an ordinary scope clarification and
  reconcile the resulting typed rows.
- Test exploration without supplying an internal namespace. The assistant must
  discover the business domain, put the source in `query.source`, inspect the
  approved semantics and follow one analysis job to completion. Correcting the
  namespace in a follow-up is a separate assisted result.
- Revoke query permission only in the isolated fixture after discovery. The
  next request must stop on denial without probing another tool/route. Compare
  execution counters before and after; a final claim of stopping does not erase
  extra calls in the trace.
- After a successful query, request a fresh read, including after isolated
  permission revocation. The assistant must not present the previous snapshot as
  proof of current freshness or current access.
- Publish a new fixture revision and submit a captured stale selection. It must
  fail before execution. Label this a scripted negative test, not a fresh user's
  natural-language success.

Keep model/provider identity, prompts, events, server trace, typed rows, job IDs,
timings and unresolved limitations in ignored local evidence. Shut down only the
test server, fixture and tunnels created for this run. These tests do not establish
real-warehouse correctness, a second human identity or first metric
entry→publication→query acceptance; verify those separately.
