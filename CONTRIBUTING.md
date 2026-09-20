# Contributing to MetricSpire

MetricSpire keeps its semantic and authorization core deliberately small. Changes should preserve explicit business meaning, bounded execution, immutable history, and provider-neutral contracts.

## Development workflow

`main` is the releasable branch. Do not push feature work directly to it.

1. Update local `main`, then create a focused branch such as `feature/release-lifecycle`, `fix/query-timezone`, or `docs/mcp-onboarding`.
2. Keep one concern per pull request. Do not mix environment receipts, generated deployment packages, or unrelated formatting changes with product code.
3. Run the local quality gate below and review `git diff --check` plus the staged file list.
4. Open a pull request. Required CI must pass before a squash merge into `main`.
5. Deploy or run environment acceptance from the reviewed commit, and record private environment evidence outside the public repository.

Repository administrators should protect `main`: require a pull request, require the `test` CI job, dismiss stale approvals after new commits, block force pushes and deletion, and allow administrators to follow the same rules. A solo maintainer may approve their own PR only when GitHub policy permits it, but must not bypass required CI.

## Local quality gate

```bash
node --test internal/httpapi/ui/app_test.js
python3 -m unittest tools/test_databricks_mcp_headers.py
go mod verify
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
git diff --check
```

Real PostgreSQL, Databricks, OAuth, and deployed-App tests are opt-in. They must use explicitly reviewed resources and must not be represented as passed when only mocks or fixtures ran.

## Product and data rules

- Public requests use structured metric codes, dimensions, filters, time bounds, and limits. Never add caller-provided SQL, identity, policy, physical bindings, or credentials.
- Published releases are immutable. A change creates a new revision and release; deactivation removes only the active pointer and preserves history.
- `trial` and `certified` are distinct release channels. Trial metrics remain visibly unverified and require an explicit deployment allowlist.
- PostgreSQL migrations are append-only. Never edit an applied migration; add a new numbered migration and test both first install and upgrade behavior.
- Any destructive environment operation starts with a read-only inventory, names the exact environment and rows/resources, and preserves an approved recovery path when the data has audit value.
- Keep credentials, private schemas, production identifiers, acceptance receipts, generated `dist/`, and local agent notes out of Git.

## Documentation

Update the closest stable guide when a public contract changes. Keep `README.md`, `README.zh-CN.md`, and `README.ja.md` aligned for top-level product claims. Historical phase logs and environment-specific evidence belong in ignored local notes, not the public documentation tree.
