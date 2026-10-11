DROP TABLE plugin_project_operation;
DROP TRIGGER project_revision ON project;
DROP FUNCTION bump_project_revision();
ALTER TABLE project DROP COLUMN revision;
