package tube

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type slotKind string

const (
	slotPassword slotKind = "password"
	slotRecovery slotKind = "recovery"
)

var (
	ErrWrongPassword    = errors.New("wrong password")
	ErrWrongRecoveryKey = errors.New("wrong recovery key")
	ErrNoKeys           = errors.New("account has no encryption keys yet")
)

func slotAD(userID int, kind slotKind) string {
	return fmt.Sprintf("slot:%d:%s", userID, kind)
}

func groupString(s string, groupLen int, sep string) string {
	if len(s) == 0 || groupLen <= 0 {
		return s
	}
	var groups []string
	for len(s) > 0 {
		n := min(groupLen, len(s))
		groups = append(groups, s[:n])
		s = s[n:]
	}
	return strings.Join(groups, sep)
}

func newRecoveryKey() string {
	raw := randomBytes(20)
	encoded := strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	return groupString(encoded, 4, "-")
}

func normalizeRecoveryKey(key string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	key = strings.NewReplacer(" ", "", "-", "").Replace(key)
	return groupString(key, 4, "-")
}

func putSlot(ctx context.Context, tx *sql.Tx, userID int, kind slotKind, salt, wrapped []byte) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO key_slots (user_id, kind, salt, wrapped, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, kind) DO UPDATE SET salt = excluded.salt, wrapped = excluded.wrapped, created = excluded.created`,
		userID, string(kind), salt, wrapped, time.Now().UTC().UnixNano())
	return err
}

func putPasswordSlot(ctx context.Context, tx *sql.Tx, userID int, k *Keyring, password string) error {
	salt := randomBytes(argonSalt)
	return putSlot(ctx, tx, userID, slotPassword, salt, wrapMaster(passwordKEK(password, salt), k, slotAD(userID, slotPassword)))
}

func putRecoverySlot(ctx context.Context, tx *sql.Tx, userID int, k *Keyring) (string, error) {
	recovery := newRecoveryKey()
	wrapped := wrapMaster(secretKEK(recovery, "recovery"), k, slotAD(userID, slotRecovery))
	return recovery, putSlot(ctx, tx, userID, slotRecovery, nil, wrapped)
}

func createKeys(ctx context.Context, tx *sql.Tx, userID int, password string) (*Keyring, string, error) {
	k := newMasterKeyring()
	if err := putPasswordSlot(ctx, tx, userID, k, password); err != nil {
		return nil, "", err
	}
	recovery, err := putRecoverySlot(ctx, tx, userID, k)
	if err != nil {
		return nil, "", err
	}
	return k, recovery, nil
}

func getSlot(ctx context.Context, q querier, userID int, kind slotKind) (salt, wrapped []byte, err error) {
	err = q.QueryRowContext(ctx,
		`SELECT salt, wrapped FROM key_slots WHERE user_id = ? AND kind = ?`, userID, string(kind)).Scan(&salt, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNoKeys
	}
	return salt, wrapped, err
}

func (u *User) Unlock(ctx context.Context, password string) error {
	if !u.ValidPassword(password) {
		return ErrWrongPassword
	}
	salt, wrapped, err := getSlot(ctx, db, u.ID, slotPassword)
	if err != nil {
		return err
	}
	k, err := unwrapMaster(passwordKEK(password, salt), wrapped, slotAD(u.ID, slotPassword))
	if err != nil {
		return ErrWrongPassword
	}
	u.keys = k
	return nil
}

func (u *User) SetupKeys(ctx context.Context, password string) (string, error) {
	if !u.ValidPassword(password) {
		return "", ErrWrongPassword
	}
	var recovery string
	err := inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := getSlot(ctx, tx, u.ID, slotPassword); err == nil {
			return fmt.Errorf("account already has encryption keys")
		} else if err != ErrNoKeys {
			return err
		}
		k, rec, err := createKeys(ctx, tx, u.ID, password)
		if err != nil {
			return err
		}
		u.keys, recovery = k, rec
		return nil
	})
	return recovery, err
}

func (u User) Unlocked() bool {
	return u.keys != nil
}

func (u User) Keys() *Keyring {
	return u.keys
}

func (u *User) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	if err := u.Unlock(ctx, oldPassword); err != nil {
		return err
	}
	hashed, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return inTx(ctx, func(tx *sql.Tx) error {
		if err := putPasswordSlot(ctx, tx, u.ID, u.keys, newPassword); err != nil {
			return err
		}
		updated, err := updateUserTx(ctx, tx, u.ID, func(x *User) error {
			x.Password = hashed
			x.Recovery = ""
			x.RecoveryExpires = time.Time{}
			return nil
		})
		if err != nil {
			return err
		}
		keys := u.keys
		*u = updated
		u.keys = keys
		return nil
	})
}

func (u *User) RecoverWithKey(ctx context.Context, recoveryKey, newPassword string) error {
	_, wrapped, err := getSlot(ctx, db, u.ID, slotRecovery)
	if err != nil {
		return err
	}
	k, err := unwrapMaster(secretKEK(normalizeRecoveryKey(recoveryKey), "recovery"), wrapped, slotAD(u.ID, slotRecovery))
	if err != nil {
		return ErrWrongRecoveryKey
	}
	return u.resetPassword(ctx, k, newPassword)
}

func (u *User) StartOver(ctx context.Context, newPassword string) (recovery string, objects []string, err error) {
	k := newMasterKeyring()
	err = inTx(ctx, func(tx *sql.Tx) error {
		objs, err := userObjects(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		objects = objs
		if err := wipeLibraryTx(ctx, tx, u.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_passwords WHERE user_id = ?`, u.ID); err != nil {
			return err
		}
		recovery, err = putRecoverySlot(ctx, tx, u.ID, k)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return recovery, objects, u.resetPassword(ctx, k, newPassword)
}

func (u *User) resetPassword(ctx context.Context, k *Keyring, newPassword string) error {
	hashed, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return inTx(ctx, func(tx *sql.Tx) error {
		if err := putPasswordSlot(ctx, tx, u.ID, k, newPassword); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID); err != nil {
			return err
		}
		updated, err := updateUserTx(ctx, tx, u.ID, func(x *User) error {
			x.Password = hashed
			x.Recovery = ""
			x.RecoveryExpires = time.Time{}
			return nil
		})
		if err != nil {
			return err
		}
		*u = updated
		u.keys = k
		return nil
	})
}

func (u *User) NewRecoveryKey(ctx context.Context) (string, error) {
	if u.keys == nil {
		return "", ErrNoKeys
	}
	var recovery string
	err := inTx(ctx, func(tx *sql.Tx) error {
		var err error
		recovery, err = putRecoverySlot(ctx, tx, u.ID, u.keys)
		return err
	})
	return recovery, err
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
