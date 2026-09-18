# Documentation

MetricSpire is a development preview. Start with the [project overview](../README.md), then choose a guide:

| Document | Audience |
| --- | --- |
| [Getting started](getting-started.md) | Run the offline semantic example and local catalog. |
| [MCP integration](mcp.md) | Connect an AI client to a deployed service. |
| [HTTP API](http-api.md) | Understand the stable request, permission, and audit boundary. |
| [Architecture](architecture.md) | Understand semantic, identity, storage, and engine boundaries. |
| [Development status](development-status.md) | Check preview maturity and unverified paths. |

The [Databricks Apps package guide](../deploy/databricks-apps/README.md) is **maintainer-specific staging documentation** beside its source, not the default installation path. The semantic schema under [`contracts/`](../contracts/metricspire.schema.json), neutral [`examples/`](../examples/orders/model.yaml), tests, license, and source code are also part of the public repository.

Phase checklists, dated test receipts, environment/operator notes, generated uploads, credentials, and customer or production data are **not** product documentation. Keep working notes in the Git-ignored `local-notes/` directory; generated builds belong in ignored `dist/`. Never commit tokens or secrets even to an ignored directory. The previously published phase notes remain reachable in Git history; moving them out of the current tree does not erase that history.
