package tube

import (
	"context"
	"database/sql"
	"fmt"
)

var backupKey []byte

func BackupKey() []byte {
	return backupKey
}

func Snapshot(ctx context.Context, path string) error {
	_, err := db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

func CheckDatabaseFile(ctx context.Context, path string) (users int, err error) {
	conn, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var result string
	if err := conn.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return 0, err
	}
	if result != "ok" {
		return 0, fmt.Errorf("integrity check: %s", result)
	}
	err = conn.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users)
	return users, err
}
