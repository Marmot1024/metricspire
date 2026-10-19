# Structured metric definitions and explicit selection

A metric can declare a reviewed `definition` alongside its executable expression.
The [synthetic orders example](../examples/metric-definitions/model.yaml) has two
members of the same family: including tests and excluding tests. Both remain
unverified until independently checked; declaring metadata does not certify a
formula or its underlying data.

| Field | Meaning |
| --- | --- |
| `family` / `variant` | Stable semantic identifiers; the pair is unique within one model |
| `population` | Included records or people |
| `exclusions` | Explicit excluded groups; `[]` means no exclusions, omission is rejected |
| `numerator` / `denominator` | Business meaning of the measured quantity and divisor; explicitly state when not applicable |
| `counting_unit` | Counting and deduplication unit |
| `time_basis` | Business date or event-time basis; actual timezone/grain still uses the reviewed dimension and binding |

Definitions contain business descriptions, not SQL or caller-supplied filters.
Each text field is bounded; exclusions are a set and compile in sorted order.
The immutable manifest fingerprint versions the complete definition, expression
and model. Physical bindings, policy and identity remain server-owned.

## Discovery and execution

1. Search authorized metrics. Search summaries include family/variant identifiers;
   they do not copy the full definition into every result.
2. Read `get_metric_details` for candidates. It returns structured definitions,
   manifest fingerprints, calendars and field differences. Legacy metrics without
   a definition appear in `missing_structured_definitions`; do not infer missing
   population or exclusion rules from a name, popularity or SQL.
3. Resolve the intended meaning. An unambiguous request can select its matching
   definition directly. If population, exclusions, denominator or counting grain
   is unresolved, present the authorized alternatives and stop for clarification.
4. Submit `metric_selections` with each selected metric name mapped to the exact
   `manifest_fingerprint` from the reviewed details, then prepare and execute.
   Keep the map in the normalized query returned by preparation.

For example, the extra query field is conceptually:

```text
"metric_selections": {
  "orders_production": "<manifest_fingerprint from get_metric_details>"
}
```

HTTP/CLI `SemanticQuery` and MCP use exact metric names. The separate online HTTP
contract uses external codes as keys, for example `"20002"`, matching
`metric_codes`. The UI displays the selected structured scope and requires an
explicit checkbox choice. A changed manifest invalidates that local choice.

After authorization, the shared planner rejects omitted choices with
`metric_selection_required` and stale/unrequested choices with
`metric_selection_changed`. Every execution plans again against the currently
resolved immutable release, including asynchronous jobs and online queries.
Fingerprints only enforce equality: they cannot select historical releases,
grant permissions or prove that a human actually confirmed the choice. A client
that submits a current fingerprint without understanding the question is still
wrong; this is not a natural-language intent classifier or a conversation log.

## Publication and compatibility

Existing metrics without `definition` continue to work. Maintainers may explicitly
add a definition through reviewed source publication. This opts those metrics into
mandatory selection, so coordinate client updates before activating that release.
If a derived metric references a structured metric, it must declare its own
structured definition; it cannot provide an unguarded alias around the selection
requirement. That declaration must accurately describe the composed formula.

Once published, a structured definition cannot change or disappear under the same
metric code. Use a new metric and variant for a different meaning. Governance
review lists definition additions, changes and removals, and rejects incompatible
changes at publication. Other model changes still produce a new manifest version,
requiring clients to review again. Authorized rollback can restore a historical
release, including a legacy release; it remains an explicit audited management
operation, not query-level fallback.

The simplified editor preserves structured definitions and locks their calculation
fields; creating/copying a new structured variant uses the reviewed source file.
No automatic migration, business-data synchronization or private semantic export
is performed by this feature.

Role and usage history may rank alternatives but never silently select a scope.
No persistent recommendation history or role-to-scope defaults are introduced.
If no authorized maintained definition fits, first confirm the unmet requirement
with the user, then use the approved exploratory workflow. Permission, timeout or
connection errors do not authorize switching routes, and an empty lexical search
alone is not proof that no metric exists.
