-- Read-only inventory of the CONNECTED database, not all project databases.
-- Sizes are storage sizes; reltuples is an estimate, never an exact row count.
BEGIN READ ONLY;
SET LOCAL statement_timeout = '10s';
SELECT current_database() AS database, current_user AS reader;
SELECT n.nspname AS schema, c.relname AS object, c.relkind,
       c.reltuples::bigint AS estimated_rows,
       CASE WHEN c.relkind IN ('r','m') THEN pg_total_relation_size(c.oid) END AS total_bytes
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg_%'
  AND c.relkind IN ('r','p','v','m')
ORDER BY 1,2;
SELECT conrelid::regclass AS source_table, conname,
       confrelid::regclass AS referenced_table, pg_get_constraintdef(oid) AS definition
FROM pg_constraint
WHERE contype='f'
  AND connamespace IN (SELECT oid FROM pg_namespace WHERE nspname <> 'information_schema' AND nspname NOT LIKE 'pg_%')
ORDER BY 1,2;
ROLLBACK;
