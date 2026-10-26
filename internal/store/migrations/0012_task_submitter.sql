-- 0012_task_submitter: who submitted a task (internal/engine Submit, README § Sign-in): the
-- signed-in user {id, name, email}, or the local user without sign-in; NULL for tasks stored
-- before this migration and for Manage's Test calls.
ALTER TABLE tasks ADD COLUMN submitted_by jsonb;
