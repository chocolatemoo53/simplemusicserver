package tube

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"time"
)

const sessionTTL = time.Hour * 24 * 7

type Session struct {
	Token   string
	UserID  int
	Expires time.Time
}

func sessionAD(hash string) string {
	return "session:" + hash
}

func CreateSession(ctx context.Context, u User) (Session, error) {
	if u.keys == nil {
		return Session{}, ErrNoKeys
	}
	token, err := randomString(64)
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	sesh := Session{
		Token:   token,
		UserID:  u.ID,
		Expires: now.Add(sessionTTL),
	}
	hash := tokenHash(token)
	wrapped := wrapMaster(secretKEK(token, "session"), u.keys, sessionAD(hash))
	err = inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires <= ?`, now.Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (token_hash, user_id, expires, wrapped) VALUES (?, ?, ?, ?)`,
			hash, sesh.UserID, sesh.Expires.Unix(), wrapped)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return sesh, nil
}

func GetSession(ctx context.Context, token string) (Session, *Keyring, error) {
	var (
		sesh    Session
		expires int64
		wrapped []byte
	)
	hash := tokenHash(token)
	err := db.QueryRowContext(ctx,
		`SELECT user_id, expires, wrapped FROM sessions WHERE token_hash = ? AND expires > ?`,
		hash, time.Now().Unix()).Scan(&sesh.UserID, &expires, &wrapped)
	if err != nil {
		return Session{}, nil, notFound(err)
	}
	k, err := unwrapMaster(secretKEK(token, "session"), wrapped, sessionAD(hash))
	if err != nil {
		return Session{}, nil, ErrNotFound
	}
	sesh.Token = token
	sesh.Expires = time.Unix(expires, 0).UTC()
	return sesh, k, nil
}

func DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE expires <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func DeleteSession(ctx context.Context, token string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
	return err
}

func DeleteOtherSessions(ctx context.Context, userID int, keepToken string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, tokenHash(keepToken))
	return err
}

func (u *User) WithKeys(k *Keyring) {
	u.keys = k
}

func randomString(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
