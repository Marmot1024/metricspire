# Phase 1 acceptance

Status: **passed on 2026-09-01**.

Phase 1 is complete only when every row below has reproducible evidence. A passing compile alone is insufficient.

| Requirement | Evidence |
| --- | --- |
| Independent Go 1.26+ clean-room module | new Git history, Apache-2.0, neutral commerce example, no legacy import or private dependency |
| Formal contracts | `contracts/metricspire.schema.json` and strict Go models for source, manifest, binding, query, context, policy, logical/physical plan, job, and problem |
| Identity separation | `SemanticQuery` has no identity fields; strict-decoder test rejects `context` and `sql` |
| Deterministic compilation | golden manifest plus 200 randomized definition-order permutations |
| Metric semantics | aggregate, ratio, and derived examples; reference/type/cycle/verification tests |
| Time semantics | explicit `[start, end)`, RFC 3339 parsing, UTC instant normalization, preserved IANA business timezone, explicit weekly start |
| Join safety | only directed `many_to_one`; missing, unsafe, and ambiguous paths reject |
| Fail-closed policy | tenant/principal/request ID required, grants required, explicit deny wins |
| Physical planning | structured resources, field binding, engine capability checks, deterministic physical fingerprint |
| Integrity | mutated manifest and logical plan reject on fingerprint mismatch |
| Robust parsing | duplicate keys, unknown fields, multiple documents, property and fuzz seed coverage |
| Build quality | `go test -race ./...`, fuzz smoke runs, `go vet ./...`, and stripped CLI build |

Final commands:

```bash
go test -race ./...
go test ./internal/contractio -run '^$' -fuzz '^FuzzDecodeJSON$' -fuzztime 3s
go test ./internal/compiler -run '^$' -fuzz '^FuzzCompileDeterministic$' -fuzztime 3s
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/metricspire ./cmd/metricspire
```

The observed results below come from these commands. Databricks execution, HTTP, MCP, typed data results, and DuckDB integration are explicitly Phase 2 or later and are not represented as Phase 1 evidence.

## Observed results — 2026-09-01

- 44 named tests passed across 7 packages on the minimum Go 1.26 toolchain.
- The same 7 packages passed with the race detector on Go 1.27.
- The 3-second parser fuzz run executed 119,651 inputs without failure; the compiler fuzz target also passed. The exact execution count is machine-dependent.
- `go vet`, `go mod verify`, the contract metaschema, all 10 example/golden artifacts, and the GitHub Actions workflow schema passed.
- The release-style binary built with `CGO_ENABLED=0`, `-trimpath`, and stripped symbols. Build metadata contains one runtime dependency: `go.yaml.in/yaml/v3 v3.0.5`.
- Statement coverage was 72.8% overall; planner and policy coverage were 80.8% and 89.2%. Coverage is recorded as evidence, not used to hide untested external execution.
- The repository has its own `main` Git history with no remote and no inherited legacy commit. GitHub repository creation and public push remain separate user-authorized actions.

Phase 1 does not prove Databricks correctness, query execution, HTTP authentication, result limits, or MCP behavior. Those remain explicit acceptance items for the next phases; no mock or DuckDB result is counted as real-engine evidence.
