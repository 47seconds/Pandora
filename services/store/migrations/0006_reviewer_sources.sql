-- Reviewers can now be managed in Pandora as well as requested on GitHub.
--   source  'github' = requested on GitHub, 'pandora' = assigned by an admin here
--   removed soft-delete: a removal sticks, so a later sync/webhook does not quietly bring the
--           reviewer back (GitHub keeps listing someone until they review, or we remove them there)
ALTER TABLE pr_reviewers ADD COLUMN source TEXT NOT NULL DEFAULT 'github';
ALTER TABLE pr_reviewers ADD COLUMN removed INTEGER NOT NULL DEFAULT 0;
