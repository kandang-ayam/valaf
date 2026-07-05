-- When no AI provider is configured (honest degradation), the notebook still
-- publishes with an analyses row of status 'skipped' and no provider/model.
-- Allow those columns to be null for that case.
ALTER TABLE analyses ALTER COLUMN provider DROP NOT NULL;
ALTER TABLE analyses ALTER COLUMN model    DROP NOT NULL;
