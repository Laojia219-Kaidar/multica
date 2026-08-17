-- name: LockWriterLeasesForCompletion :many
-- Migration 262 is the sole writer-lease authority. The terminal fence locks
-- every authoritative lease row in canonical key order before it accepts a
-- daemon terminal proof.
SELECT mutex_key, holder_id, lease_token, fence_generation,
       status, expires_at, CASE WHEN expires_at > clock_timestamp() THEN true ELSE false END AS not_expired
FROM write_lease
WHERE mutex_key = ANY(sqlc.arg('mutex_keys')::text[])
ORDER BY mutex_key ASC
FOR UPDATE;
