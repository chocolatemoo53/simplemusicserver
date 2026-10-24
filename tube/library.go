package tube

import (
	"context"
	"database/sql"
)

func userObjects(ctx context.Context, q querier, userID int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM objects WHERE user_id = ? ORDER BY key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		objects = append(objects, key)
	}
	return objects, rows.Err()
}

func wipeLibraryTx(ctx context.Context, tx *sql.Tx, userID int) error {
	for _, table := range []string{"tracks", "playlists", "stars", "files", "objects"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	_, err := updateUserTx(ctx, tx, userID, func(u *User) error {
		u.Usage = 0
		u.Tracks = 0
		return nil
	})
	return err
}

func WipeLibrary(ctx context.Context, userID int) error {
	return inTx(ctx, func(tx *sql.Tx) error {
		return wipeLibraryTx(ctx, tx, userID)
	})
}
