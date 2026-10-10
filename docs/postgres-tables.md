# PostgreSQL table responsibilities

The control store holds configuration, immutable releases, governance records and audit, not analytical fact data. Its current migrations create eleven tables including the migration ledger. They are not eleven per-metric business tables.

| Group | Tables | Why separate |
| --- | --- | --- |
| Model/release (5) | `metricspire_drafts`, `metricspire_draft_revisions`, `metricspire_releases`, `metricspire_active_releases`, `metricspire_release_events` | Mutable draft, revision history, immutable executable release, active pointer, and lifecycle evidence. |
| Governance import (4) | `metricspire_governance_imports`, `metricspire_governance_records`, `metricspire_governance_record_revisions`, `metricspire_governance_import_items` | Import receipt, editable current record, revision history and per-import mapping. Imported records do not automatically become executable releases. |
| Query audit (1) | `metricspire_query_audit` | Durable query start/completion evidence. |
| Migration ledger (1) | `metricspire_schema_migrations` | Tracks applied control-store migrations. |

Current/history pairs serve different lifecycle guarantees. A low row count, old namespace or absent recent table-access counter is insufficient evidence that a table is unused. Removing a table requires checking runtime queries, migration compatibility, foreign keys, operators/consumers and retention requirements, then a separately reviewed migration and recovery plan.

New online business aggregates belong in a separate reviewed serving schema/database, never in these control tables and never one table per metric code. Metrics sharing the same grain and access/update boundary can share an aggregate table. Different grain or non-additive business definitions need an explicit decision, not a cosmetic table merge.

The read-only [inventory SQL](../tools/postgres-inventory.sql) lists attached-database schemas, tables, sizes and foreign keys. Use it before making cleanup decisions. Namespace/test-data retirement and audit/history retention still need a documented owner and retention policy; this guide does not authorize deletion.
