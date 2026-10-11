ALTER TABLE project ADD COLUMN revision BIGINT NOT NULL DEFAULT 1;
CREATE FUNCTION bump_project_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.revision := OLD.revision + 1; RETURN NEW; END;
$$;
CREATE TRIGGER project_revision BEFORE UPDATE ON project FOR EACH ROW EXECUTE FUNCTION bump_project_revision();
-- Retain operation receipts even when projects are deleted: retry must never recreate them.
CREATE TABLE plugin_project_operation (
 installation_id UUID NOT NULL,
 operation_id UUID NOT NULL,
 request JSONB NOT NULL,
 response JSONB NOT NULL
);
