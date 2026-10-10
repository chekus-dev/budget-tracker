-- 014_amount_must_be_positive.sql
--
-- The sign of a money figure is carried by `kind`, not by the amount column.
-- Every aggregate in the app assumes amount is a positive magnitude; a
-- negative row would silently invert a budget calculation rather than
-- announcing itself as a data problem.
--
-- Named constraint so it can be dropped or altered by name later.

ALTER TABLE expenses
    ADD CONSTRAINT expenses_amount_positive CHECK (amount > 0);