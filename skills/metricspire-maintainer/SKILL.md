---
name: metricspire-maintainer
description: Add or revise MetricSpire metric definitions through source validation, draft review, publication and reader-result reconciliation. Use for metric maintenance or onboarding, not ordinary metric queries or database administration.
---

# MetricSpire metric maintenance

Locate the user's MetricSpire checkout and read `docs/metric-maintenance.md`.
For service prerequisites read `docs/self-hosted.md`; for temporary questions
read `docs/semantic-layer.md`. If no checkout is available, use the
[maintainer guide](https://github.com/marmot1024/metricspire/blob/main/docs/metric-maintenance.md)
and ask for the target instance/model before making changes. Do not assume
that query MCP tools can manage drafts or grants.

1. Establish the metric's owner, population, exclusions, counting grain,
   date/timezone, unit and formula. Reuse an existing definition when it answers
   the request. Ask only about unresolved choices that affect the result.
2. Find the actual maintenance path: web workbench, authenticated management
   API or trusted operator CLI. Reuse existing authorization. Query permission
   does not imply `model:manage`; never invent credentials or caller identity.
3. Keep company definitions and field mappings in private/ignored storage.
   Validate the candidate `SemanticModel` source. Structured variants and new
   datasets need reviewed source; the simplified web editor is not a schema editor.
4. Read the current draft and active release. Save using the actual
   `expected_revision`, review the diff and validate the binding. On conflict,
   reload and compare; do not overwrite another revision. Published formula or
   scope changes require a new metric identity, not replacement of its history.
5. Follow the user's publication authorization. When only a draft/review was
   requested, stop at that reviewable candidate. When publication is authorized,
   publish the exact saved revision and record its release ID. Do not infer
   deployment, grants or upstream refresh authority from metric maintenance.
6. Verify queryability using the intended route and reader identity. Online
   serving needs a compatible binding, independent policy and a reconciled data
   snapshot; publication alone creates no result data. Check typed values,
   grain, release and snapshot against an independent result, plus a denied
   reader when that environment check is authorized.

Report source validation, publication, data reconciliation and intended-user
acceptance separately. Name anything untested. A plan, a mock response or an
administrator's successful query is not evidence of an ordinary user's result.
Do not install this skill or alter client configuration unless requested.
