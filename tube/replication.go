package tube

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	ReplicaCopy   = "copy"
	ReplicaCopied = "copied"
	ReplicaErase  = "erase"
)

type ReplicaJob struct {
	Key         string
	State       string
	RetainUntil time.Time
	Attempts    int
}

type ReplicaStats struct {
	Copied  int
	Pending int
	Erasing int
	Failing int
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func ReconcileReplicas(ctx context.Context, now time.Time, minAge time.Duration) error {
	ts := now.Unix()
	return inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO replicas (key, state, next_at) SELECT key, ?, ? FROM objects WHERE (? = 0 OR created <= ?) AND key NOT IN (SELECT key FROM replicas)`,
			ReplicaCopy, ts, minAge, now.Add(-minAge).UnixNano()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE replicas SET state = ?, next_at = ?, attempts = 0, error = '' WHERE state = ? AND key IN (SELECT key FROM objects)`,
			ReplicaCopy, ts, ReplicaErase); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE replicas SET state = ?, next_at = ?, attempts = 0, error = '' WHERE state != ? AND key NOT IN (SELECT key FROM objects)`,
			ReplicaErase, ts, ReplicaErase)
		return err
	})
}

func ClaimReplicaJob(ctx context.Context, now time.Time, lease time.Duration) (ReplicaJob, bool, error) {
	var job ReplicaJob
	var retain int64
	err := db.QueryRowContext(ctx,
		`UPDATE replicas SET lease_until = ?
		WHERE key = (SELECT key FROM replicas WHERE state != ? AND next_at <= ? AND lease_until <= ? ORDER BY next_at LIMIT 1)
		RETURNING key, state, retain_until, attempts`,
		now.Add(lease).Unix(), ReplicaCopied, now.Unix(), now.Unix()).Scan(&job.Key, &job.State, &retain, &job.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return job, false, nil
	}
	if err != nil {
		return job, false, err
	}
	job.RetainUntil = timeOrZero(retain)
	return job, true, nil
}

func CopiedReplica(ctx context.Context, key string, retainUntil, now time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE replicas SET
			state = CASE WHEN state = ? THEN ? ELSE state END,
			retain_until = MAX(retain_until, ?), verified = ?, lease_until = 0, attempts = 0, error = ''
		WHERE key = ?`,
		ReplicaCopy, ReplicaCopied, unixOrZero(retainUntil), now.Unix(), key)
	return err
}

func RetryReplica(ctx context.Context, key string, attempts int, now time.Time, cause error) error {
	backoff := time.Minute << min(attempts, 9)
	backoff = min(backoff, 6*time.Hour)
	_, err := db.ExecContext(ctx,
		`UPDATE replicas SET attempts = ?, next_at = ?, lease_until = 0, error = ? WHERE key = ?`,
		attempts+1, now.Add(backoff).Unix(), cause.Error(), key)
	return err
}

func PostponeReplica(ctx context.Context, key string, until time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE replicas SET next_at = ?, lease_until = 0 WHERE key = ?`, until.Unix(), key)
	return err
}

func ErasedReplica(ctx context.Context, key string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM replicas WHERE key = ? AND state = ?`, key, ReplicaErase)
	return err
}

func replicaKeys(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func ReplicasToExtend(ctx context.Context, before time.Time, limit int) ([]string, error) {
	return replicaKeys(ctx,
		`SELECT key FROM replicas WHERE state = ? AND retain_until < ? ORDER BY retain_until LIMIT ?`,
		ReplicaCopied, before.Unix(), limit)
}

func ExtendedReplica(ctx context.Context, key string, until time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE replicas SET retain_until = MAX(retain_until, ?) WHERE key = ?`, until.Unix(), key)
	return err
}

func ReplicasToVerify(ctx context.Context, before time.Time, limit int) ([]string, error) {
	return replicaKeys(ctx,
		`SELECT key FROM replicas WHERE state = ? AND verified < ? ORDER BY verified LIMIT ?`,
		ReplicaCopied, before.Unix(), limit)
}

func VerifiedReplica(ctx context.Context, key string, now time.Time, ok bool) error {
	if ok {
		_, err := db.ExecContext(ctx, `UPDATE replicas SET verified = ? WHERE key = ?`, now.Unix(), key)
		return err
	}
	_, err := db.ExecContext(ctx,
		`UPDATE replicas SET state = ?, next_at = ?, verified = 0 WHERE key = ? AND state = ?`,
		ReplicaCopy, now.Unix(), key, ReplicaCopied)
	return err
}

func GetReplicaStats(ctx context.Context) (ReplicaStats, error) {
	var s ReplicaStats
	err := db.QueryRowContext(ctx,
		`SELECT
			COALESCE(SUM(state = ?), 0), COALESCE(SUM(state = ?), 0), COALESCE(SUM(state = ?), 0), COALESCE(SUM(attempts > 0), 0)
		FROM replicas`,
		ReplicaCopied, ReplicaCopy, ReplicaErase).Scan(&s.Copied, &s.Pending, &s.Erasing, &s.Failing)
	return s, err
}
