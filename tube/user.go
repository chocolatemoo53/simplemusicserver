package tube

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID              int
	Email           string
	Password        []byte `json:"-"`
	Regdate         time.Time
	Recovery        string
	RecoveryExpires time.Time

	EmailVerified time.Time
	VerifyToken   string
	VerifyExpires time.Time

	Usage  int64
	Tracks int

	Theme   string
	Display DisplayOptions

	PGPKey         string
	PGPFingerprint string

	LastMod time.Time

	keys *Keyring
}

type DisplayOptions struct {
	Stretch      bool
	TrackSelect  TrackSelOpt
	LyricsLookup bool
}

func (u *User) Create(ctx context.Context, password string) (string, error) {
	if u.ID != 0 {
		return "", fmt.Errorf("user already exists: %d", u.ID)
	}
	hashed, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	u.Password = hashed
	u.Regdate = time.Now().UTC()
	u.Email = strings.ToLower(u.Email)

	var recovery string
	err = inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO users (email, password, data) VALUES (?, ?, '{}')`, u.Email, u.Password)
		if isUniqueViolation(err) {
			return fmt.Errorf("e-mail already in use: %s", u.Email)
		}
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		u.ID = int(id)
		if err := saveUser(ctx, tx, *u); err != nil {
			return err
		}
		u.keys, recovery, err = createKeys(ctx, tx, u.ID, password)
		return err
	})
	if err != nil {
		u.ID = 0
		return "", err
	}
	return recovery, nil
}

func (u *User) SetEmail(ctx context.Context, email string) error {
	if email == "" {
		return fmt.Errorf("blank email")
	}
	email = strings.ToLower(email)
	err := u.update(ctx, func(u *User) {
		u.Email = email
		u.EmailVerified = time.Time{}
		u.VerifyToken = ""
		u.VerifyExpires = time.Time{}
		u.LastMod = time.Now().UTC()
	})
	if isUniqueViolation(err) {
		return fmt.Errorf("e-mail already in use: %s", email)
	}
	return err
}

const recoveryTTL = time.Hour

func (u *User) StartRecovery(ctx context.Context) (string, error) {
	token, err := randomString(48)
	if err != nil {
		return "", err
	}
	err = u.update(ctx, func(u *User) {
		u.Recovery = tokenHash(token)
		u.RecoveryExpires = time.Now().UTC().Add(recoveryTTL)
	})
	return token, err
}

func (u User) ValidRecovery(token string, now time.Time) bool {
	return u.Recovery != "" && now.Before(u.RecoveryExpires) && EqualSecret(u.Recovery, tokenHash(token))
}

var RequireVerifiedEmail bool

var ErrBadVerifyToken = errors.New("invalid or expired verification link")

const verifyTTL = 7 * 24 * time.Hour

func (u User) Verified() bool {
	return !u.EmailVerified.IsZero()
}

func (u User) NeedsVerification() bool {
	return RequireVerifiedEmail && !u.Verified()
}

func (u *User) StartVerification(ctx context.Context) (string, error) {
	token, err := randomString(48)
	if err != nil {
		return "", err
	}
	err = u.update(ctx, func(u *User) {
		u.VerifyToken = tokenHash(token)
		u.VerifyExpires = time.Now().UTC().Add(verifyTTL)
	})
	return token, err
}

func VerifyEmail(ctx context.Context, userID int, token string, now time.Time) error {
	_, err := updateUser(ctx, userID, func(u *User) error {
		if u.Verified() {
			return nil
		}
		if u.VerifyToken == "" || !now.Before(u.VerifyExpires) || !EqualSecret(u.VerifyToken, tokenHash(token)) {
			return ErrBadVerifyToken
		}
		u.EmailVerified = now.UTC()
		u.VerifyToken = ""
		u.VerifyExpires = time.Time{}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrBadVerifyToken
	}
	return err
}

func (u *User) UpdateLastMod(ctx context.Context) error {
	now := time.Now().UTC().Truncate(time.Second)
	return u.update(ctx, func(u *User) {
		u.LastMod = now
	})
}

func (u *User) SetTheme(ctx context.Context, theme string) error {
	return u.update(ctx, func(u *User) {
		u.Theme = theme
		u.LastMod = time.Now().UTC()
	})
}

func (u *User) SetPGPKey(ctx context.Context, armored, fingerprint string) error {
	return u.update(ctx, func(u *User) {
		u.PGPKey = armored
		u.PGPFingerprint = fingerprint
	})
}

func (u *User) SetDisplayOpt(ctx context.Context, disp DisplayOptions) error {
	return u.update(ctx, func(u *User) {
		u.Display = disp
		u.LastMod = time.Now().UTC()
	})
}

func (u *User) update(ctx context.Context, fn func(*User)) error {
	updated, err := updateUser(ctx, u.ID, func(u *User) error {
		fn(u)
		return nil
	})
	if err != nil {
		return err
	}
	keys := u.keys
	*u = updated
	u.keys = keys
	return nil
}

func updateUser(ctx context.Context, id int, fn func(*User) error) (User, error) {
	var out User
	err := inTx(ctx, func(tx *sql.Tx) error {
		u, err := updateUserTx(ctx, tx, id, fn)
		out = u
		return err
	})
	return out, err
}

func updateUserTx(ctx context.Context, tx *sql.Tx, id int, fn func(*User) error) (User, error) {
	u, err := getUser(ctx, tx, "id = ?", id)
	if err != nil {
		return User{}, err
	}
	if err := fn(&u); err != nil {
		return User{}, err
	}
	return u, saveUser(ctx, tx, u)
}

func saveUser(ctx context.Context, q querier, u User) error {
	data, err := encode(u)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE users SET email = ?, password = ?, data = ? WHERE id = ?`, u.Email, u.Password, data, u.ID)
	return err
}

const userColumns = "id, email, password, data"

func scanUser(s scanner) (User, error) {
	var (
		id             int
		email          string
		password, data []byte
	)
	if err := s.Scan(&id, &email, &password, &data); err != nil {
		return User{}, notFound(err)
	}
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return User{}, err
	}
	u.ID, u.Email, u.Password = id, email, password
	return u, nil
}

func getUser(ctx context.Context, q querier, where string, args ...any) (User, error) {
	return scanUser(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE "+where, args...))
}

func (u User) ValidPassword(password string) bool {
	return bcrypt.CompareHashAndPassword(u.Password, []byte(password)) == nil
}

func GetUser(ctx context.Context, id int) (User, error) {
	return getUser(ctx, db, "id = ?", id)
}

func GetUserByEmail(ctx context.Context, email string) (User, error) {
	return getUser(ctx, db, "email = ?", strings.ToLower(email))
}

func GetAllUsers(ctx context.Context) ([]User, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func AddUsage(ctx context.Context, id int, usage int64, trackCt int) (User, error) {
	return updateUser(ctx, id, addUsage(usage, trackCt))
}

func addUsage(usage int64, trackCt int) func(*User) error {
	return func(u *User) error {
		u.Usage += usage
		u.Tracks += trackCt
		return nil
	}
}

func HashPassword(password string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

type TrackSelOpt string

const (
	TrackSelDefault TrackSelOpt = ""
	TrackSelCtrlKey TrackSelOpt = "ctrl"
)

func (tso TrackSelOpt) String() string {
	return string(tso)
}
