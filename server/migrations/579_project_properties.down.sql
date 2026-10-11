-- Revert project-level custom properties. Project values disappear with the
-- column; integration-owned project definitions lose their discriminator and
-- ownership column. Existing project rows cannot exceed the checked bag
-- because the constraints were validated before application.
ALTER TABLE issue_property DROP COLUMN IF EXISTS owner_installation_id;

ALTER TABLE issue_property DROP COLUMN IF EXISTS resource_type;

ALTER TABLE project DROP CONSTRAINT IF EXISTS project_properties_size_limit;
ALTER TABLE project DROP CONSTRAINT IF EXISTS project_properties_is_object;

ALTER TABLE project DROP COLUMN IF EXISTS properties;
