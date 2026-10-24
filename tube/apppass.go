package tube

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxAppPasswords     = 20
	maxAppPasswordName  = 50
	appPasswordBytes    = 20
	appPasswordGroupLen = 4
	lastUsedGranularity = 24 * time.Hour
)

var (
	ErrTooManyAppPasswords = fmt.Errorf("you can have at most %d app passwords", MaxAppPasswords)
	ErrStrictAppPassword   = errors.New("this app password is strict and can't be used with token login")
	errNoSecretKey         = errors.New("app passwords need secret_key to be configured")
)

var (
	appPasswordAEAD      cipher.AEAD
	appPasswordLookupKey []byte
)

func SetSecretKey(key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("app-password-lookup"))
	appPasswordAEAD = aead
	appPasswordLookupKey = mac.Sum(nil)
	backupKey = deriveKey(key, "simplemusicserver database backup")
	return nil
}

func lookupFor(secret string) string {
	mac := hmac.New(sha256.New, appPasswordLookupKey)
	mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil))
}

type AppPassword struct {
	ID       int
	UserID   int
	Name     string
	Strict   bool
	Created  time.Time
	LastUsed time.Time
}

func newAppPasswordSecret() string {
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes(appPasswordBytes)))
	return groupString(encoded, appPasswordGroupLen, "-")
}

func sealServerSecret(userID int, plaintext string) ([]byte, error) {
	if appPasswordAEAD == nil {
		return nil, errNoSecretKey
	}
	nonce := make([]byte, appPasswordAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return appPasswordAEAD.Seal(nonce, nonce, []byte(plaintext), serverSecretAD(userID)), nil
}

func openServerSecret(userID int, sealed []byte) (string, error) {
	if appPasswordAEAD == nil {
		return "", errNoSecretKey
	}
	n := appPasswordAEAD.NonceSize()
	if len(sealed) < n {
		return "", ErrDecrypt
	}
	plain, err := appPasswordAEAD.Open(nil, sealed[:n], sealed[n:], serverSecretAD(userID))
	return string(plain), err
}

func serverSecretAD(userID int) []byte {
	return fmt.Appendf(nil, "app-password:%d", userID)
}

func appSlotAD(userID, id int) string {
	return fmt.Sprintf("app-slot:%d:%d", userID, id)
}

func (u User) CreateAppPassword(ctx context.Context, name string, strict bool) (AppPassword, string, error) {
	if u.keys == nil {
		return AppPassword{}, "", ErrLocked
	}
	if appPasswordAEAD == nil {
		return AppPassword{}, "", errNoSecretKey
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxAppPasswordName {
		return AppPassword{}, "", fmt.Errorf("give the app password a name of 1 to %d characters", maxAppPasswordName)
	}
	secret := newAppPasswordSecret()
	var serverCopy []byte
	if !strict {
		var err error
		if serverCopy, err = sealServerSecret(u.ID, secret); err != nil {
			return AppPassword{}, "", err
		}
	}
	ap := AppPassword{UserID: u.ID, Name: name, Strict: strict, Created: time.Now().UTC()}
	err := inTx(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_passwords WHERE user_id = ?`, u.ID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxAppPasswords {
			return ErrTooManyAppPasswords
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO app_passwords (user_id, name, strict, secret, lookup, wrapped, created) VALUES (?, ?, ?, ?, ?, x'', ?)`,
			u.ID, name, strict, serverCopy, lookupFor(secret), ap.Created.UnixNano())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		ap.ID = int(id)
		wrapped := wrapMaster(secretKEK(secret, "app"), u.keys, appSlotAD(u.ID, ap.ID))
		_, err = tx.ExecContext(ctx, `UPDATE app_passwords SET wrapped = ? WHERE id = ?`, wrapped, ap.ID)
		return err
	})
	if err != nil {
		return AppPassword{}, "", err
	}
	return ap, secret, nil
}

func ListAppPasswords(ctx context.Context, userID int) ([]AppPassword, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, name, strict, created, last_used FROM app_passwords WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []AppPassword
	for rows.Next() {
		var (
			ap                AppPassword
			created, lastUsed int64
		)
		if err := rows.Scan(&ap.ID, &ap.UserID, &ap.Name, &ap.Strict, &created, &lastUsed); err != nil {
			return nil, err
		}
		ap.Created = time.Unix(0, created).UTC()
		if lastUsed != 0 {
			ap.LastUsed = time.Unix(0, lastUsed).UTC()
		}
		list = append(list, ap)
	}
	return list, rows.Err()
}

func RevokeAppPassword(ctx context.Context, userID, id int) error {
	res, err := db.ExecContext(ctx, `DELETE FROM app_passwords WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func unlockAppPassword(ap AppPassword, secret string, wrapped []byte) (*Keyring, error) {
	k, err := unwrapMaster(secretKEK(secret, "app"), wrapped, appSlotAD(ap.UserID, ap.ID))
	if err != nil {
		return nil, ErrNotFound
	}
	return k, nil
}

func UnlockWithAppPassword(ctx context.Context, secret string) (AppPassword, *Keyring, error) {
	if appPasswordAEAD == nil {
		return AppPassword{}, nil, errNoSecretKey
	}
	var (
		ap      AppPassword
		wrapped []byte
	)
	err := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, strict, wrapped FROM app_passwords WHERE lookup = ?`, lookupFor(secret)).
		Scan(&ap.ID, &ap.UserID, &ap.Name, &ap.Strict, &wrapped)
	if err != nil {
		return AppPassword{}, nil, notFound(err)
	}
	k, err := unlockAppPassword(ap, secret, wrapped)
	if err != nil {
		return AppPassword{}, nil, err
	}
	return ap, k, touchAppPassword(ctx, ap.ID)
}

func UnlockWithAppPasswordToken(ctx context.Context, userID int, matches func(secret string) bool) (AppPassword, *Keyring, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, strict, secret, wrapped FROM app_passwords WHERE user_id = ?`, userID)
	if err != nil {
		return AppPassword{}, nil, err
	}
	type candidate struct {
		ap      AppPassword
		sealed  []byte
		wrapped []byte
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ap.ID, &c.ap.Name, &c.ap.Strict, &c.sealed, &c.wrapped); err != nil {
			rows.Close()
			return AppPassword{}, nil, err
		}
		c.ap.UserID = userID
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AppPassword{}, nil, err
	}
	sawStrict := false
	for _, c := range candidates {
		if c.ap.Strict {
			sawStrict = true
			continue
		}
		secret, err := openServerSecret(userID, c.sealed)
		if err != nil || !matches(secret) {
			continue
		}
		k, err := unlockAppPassword(c.ap, secret, c.wrapped)
		if err != nil {
			return AppPassword{}, nil, err
		}
		return c.ap, k, touchAppPassword(ctx, c.ap.ID)
	}
	if sawStrict {
		return AppPassword{}, nil, ErrStrictAppPassword
	}
	return AppPassword{}, nil, ErrNotFound
}

func touchAppPassword(ctx context.Context, id int) error {
	today := time.Now().UTC().Truncate(lastUsedGranularity)
	_, err := db.ExecContext(ctx,
		`UPDATE app_passwords SET last_used = ? WHERE id = ? AND last_used < ?`,
		today.UnixNano(), id, today.UnixNano())
	return err
}

func EqualSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
