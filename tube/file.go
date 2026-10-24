package tube

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strconv"
	"time"
)

type File struct {
	ID       string
	UserID   int
	Name     string
	Ext      string
	Type     string
	Size     int64
	LocalMod int64
	Time     time.Time
	TrackID  string
}

func fileAD(userID int, id string) string {
	return fmt.Sprintf("files:%d:%s", userID, id)
}

func NewFile(userID int, filename string, size int64) File {
	now := time.Now().UTC()
	garb, err := randomString(8)
	if err != nil {
		panic(err)
	}
	return File{
		ID:     strconv.FormatInt(now.UnixNano(), 36) + "-" + garb,
		UserID: userID,
		Name:   filename,
		Ext:    path.Ext(filename),
		Size:   size,
		Time:   now,
	}
}

func (u User) saveFile(ctx context.Context, q querier, f File) error {
	if u.keys == nil {
		return ErrLocked
	}
	data, err := u.keys.SealJSON(f, fileAD(u.ID, f.ID))
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO files (id, user_id, created, data) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET data = excluded.data WHERE files.user_id = excluded.user_id`,
		f.ID, u.ID, f.Time.UnixNano(), data)
	return err
}

func (u User) CreateFile(ctx context.Context, f File) error {
	f.UserID = u.ID
	return u.saveFile(ctx, db, f)
}

func (u User) GetFile(ctx context.Context, id string) (File, error) {
	if u.keys == nil {
		return File{}, ErrLocked
	}
	var data []byte
	err := db.QueryRowContext(ctx, `SELECT data FROM files WHERE id = ? AND user_id = ?`, id, u.ID).Scan(&data)
	if err != nil {
		return File{}, notFound(err)
	}
	var f File
	if err := u.keys.OpenJSON(data, fileAD(u.ID, id), &f); err != nil {
		return File{}, err
	}
	return f, nil
}

func (u User) SetFileTrack(ctx context.Context, id, trackID string) error {
	return inTx(ctx, func(tx *sql.Tx) error {
		f, err := u.GetFile(ctx, id)
		if err != nil {
			return err
		}
		f.TrackID = trackID
		return u.saveFile(ctx, tx, f)
	})
}

func DeleteStaleFiles(ctx context.Context, before time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM files WHERE created < ?`, before.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (f File) Status() string {
	if f.TrackID != "" {
		return "done"
	}
	return "uploading"
}
