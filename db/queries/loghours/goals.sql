-- S3-03: goal targets (CLAUDE.md §7.2, FR-GOAL-02). Static SQL only (SEC-20, SEC-21); every statement
-- carries the owner's user_id (GO-47), so another user's goals match no row.

-- name: ListGoals :many
-- The user's four goal rows (S3-01 creates them with the users row), in display order.
SELECT goal_type, target_hours, last_celebrated_percent
FROM goals
WHERE user_id = sqlc.arg(user_id)
ORDER BY CASE goal_type
    WHEN 'direct' THEN 1
    WHEN 'supervision' THEN 2
    WHEN 'total_activity' THEN 3
    ELSE 4
END;

-- name: UpdateGoalTarget :one
-- On-site has no target (goals_target_presence_chk), so it is never updated: the goal_type filter
-- makes that true even if a caller forgets to check. last_celebrated_percent is left alone.
UPDATE goals
SET target_hours = sqlc.arg(target_hours)
WHERE user_id = sqlc.arg(user_id)
  AND goal_type = sqlc.arg(goal_type)
  AND goal_type <> 'on_site'
RETURNING goal_type, target_hours, last_celebrated_percent;
