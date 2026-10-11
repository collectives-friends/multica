-- Property names become unique per resource type (see 581). Drop the old
-- workspace-wide unique name index first so issue and project properties can
-- share a name in the same workspace. Single-statement file: DROP INDEX
-- CONCURRENTLY cannot share a migration, and the rebuild lives in 581.
DROP INDEX CONCURRENTLY IF EXISTS idx_issue_property_ws_name;
