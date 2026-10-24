package tube

import (
	"context"
	"fmt"
	"time"
)

type Star struct {
	UserID int `json:"-"`
	SSID   SSID
	Date   time.Time
}

func starAD(userID int, key string) string {
	return fmt.Sprintf("stars:%d:%s", userID, key)
}

func (u User) starKey(ssid SSID) string {
	return u.keys.ID("star", ssid.String())
}

func (u User) SetStar(ctx context.Context, ssid SSID, date time.Time) error {
	if u.keys == nil {
		return ErrLocked
	}
	key := u.starKey(ssid)
	data, err := u.keys.SealJSON(Star{SSID: ssid, Date: date}, starAD(u.ID, key))
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO stars (user_id, key, data) VALUES (?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET data = excluded.data`,
		u.ID, key, data)
	return err
}

func (u User) DeleteStar(ctx context.Context, ssid string) error {
	if u.keys == nil {
		return ErrLocked
	}
	_, err := db.ExecContext(ctx, `DELETE FROM stars WHERE user_id = ? AND key = ?`, u.ID, u.starKey(ParseSSID(ssid)))
	return err
}

func (u User) GetStars(ctx context.Context) (map[SSID]Star, error) {
	if u.keys == nil {
		return nil, ErrLocked
	}
	rows, err := db.QueryContext(ctx, `SELECT key, data FROM stars WHERE user_id = ?`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stars := make(map[SSID]Star)
	for rows.Next() {
		var (
			key  string
			data []byte
		)
		if err := rows.Scan(&key, &data); err != nil {
			return nil, err
		}
		var s Star
		if err := u.keys.OpenJSON(data, starAD(u.ID, key), &s); err != nil {
			return nil, err
		}
		s.UserID = u.ID
		stars[s.SSID] = s
	}
	return stars, rows.Err()
}
