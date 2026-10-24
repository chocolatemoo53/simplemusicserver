package tube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	ObjectTrack   = "track"
	ObjectPicture = "picture"
)

func recordObject(ctx context.Context, q querier, userID int, key, kind string, size int64) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO objects (key, user_id, kind, size, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO NOTHING`,
		key, userID, kind, size, time.Now().UTC().UnixNano())
	return err
}

func RecordObject(ctx context.Context, userID int, key, kind string, size int64) error {
	return recordObject(ctx, db, userID, key, kind, size)
}

func ObjectSize(ctx context.Context, userID int, key string) (int64, error) {
	var size int64
	err := db.QueryRowContext(ctx, `SELECT size FROM objects WHERE key = ? AND user_id = ?`, key, userID).Scan(&size)
	return size, notFound(err)
}

func UserObjects(ctx context.Context, userID int) ([]string, error) {
	return userObjects(ctx, db, userID)
}

func (u User) PictureFor(data []byte, ext, mimetype, desc string) Picture {
	sum := sha256.Sum256(data)
	id := u.keys.ID("picture", hex.EncodeToString(sum[:]))
	return Picture{
		ID:     id,
		Type:   mimetype,
		Ext:    ext,
		Desc:   desc,
		Object: PictureObject(u.ID, id),
		Size:   int64(len(data)),
	}
}

func PictureObject(userID int, id string) string {
	return fmt.Sprintf("u/%d/p/%s", userID, id)
}

func (u User) PictureKey(id string) []byte {
	return deriveKey(u.keys.fileKEK, "picture "+id)
}

func (u User) TrackFileKey(t Track) ([]byte, error) {
	if u.keys == nil {
		return nil, ErrLocked
	}
	return u.keys.FileKey(t.FileKey, "file:"+t.Object)
}

func (u User) NewTrackFileKey(object string) (fileKey, wrapped []byte, err error) {
	if u.keys == nil {
		return nil, nil, ErrLocked
	}
	fileKey, wrapped = u.keys.NewFileKey("file:" + object)
	return fileKey, wrapped, nil
}
