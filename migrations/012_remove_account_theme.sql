-- Appearance is a browser preference, not account data. The application no
-- longer reads or writes this column, so remove the obsolete setting.
ALTER TABLE settings DROP COLUMN IF EXISTS theme;
