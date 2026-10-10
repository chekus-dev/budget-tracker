-- 013_unique_identity_case_insensitive.sql
--
-- The UNIQUE constraints on username and email are case-sensitive, which
-- means 'Alice' and 'alice' can both register, and a user who signs up as
-- 'Alice' cannot log in as 'alice'. Email has the same problem, which is
-- worse: a password reset sent to 'User@x.com' will not match a row stored
-- as 'user@x.com'.
--
-- Replace both constraints with case-insensitive functional indexes. The
-- columns keep their original values; only the uniqueness check changes.
--
-- The pre-flight check exists because CREATE UNIQUE INDEX fails on a table
-- that already contains a collision, and the error it raises names the index
-- rather than the data. This block names the problem and the count, so an
-- operator knows to resolve it rather than re-run the migration and hope.

DO $$
DECLARE
    dup_username int;
    dup_email int;
BEGIN
    SELECT COUNT(*) INTO dup_username FROM (
        SELECT 1 FROM users GROUP BY LOWER(username) HAVING COUNT(*) > 1
    ) d;
    SELECT COUNT(*) INTO dup_email FROM (
        SELECT 1 FROM users
        WHERE email IS NOT NULL
        GROUP BY LOWER(email) HAVING COUNT(*) > 1
    ) d;

    IF dup_username > 0 OR dup_email > 0 THEN
        RAISE EXCEPTION
            'Cannot apply 013: % username case-collision(s), % email case-collision(s). '
            'Rename or merge the duplicated accounts, then re-run migrations.',
            dup_username, dup_email;
    END IF;
END $$;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_username_key;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;

CREATE UNIQUE INDEX IF NOT EXISTS users_username_lower_idx
    ON users (LOWER(username));

CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_idx
    ON users (LOWER(email))
    WHERE email IS NOT NULL;