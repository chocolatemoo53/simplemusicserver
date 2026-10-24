package tube

import (
	"context"
	"fmt"
	"time"
)

type Totals struct {
	Users    int
	Paying   int
	Lapsed   int
	Usage    int64
	Tracks   int
	Balances Micros
	Monthly  Micros
}

func SumUsers(users []User, now time.Time) Totals {
	var t Totals
	for _, u := range users {
		t.Users++
		t.Usage += u.Usage
		t.Tracks += u.Tracks
		t.Balances += max(0, u.Balance)
		if cost := u.MonthlyCost(); cost > 0 {
			t.Paying++
			t.Monthly += cost
		}
		if u.State(now) != StateActive {
			t.Lapsed++
		}
	}
	return t
}

func SetExempt(ctx context.Context, userID int, exempt bool) error {
	_, err := updateUser(ctx, userID, func(u *User) error {
		u.Exempt = exempt
		if exempt {
			u.LapsedAt = time.Time{}
			u.Notified = 0
			u.NotifiedAt = time.Time{}
		}
		return nil
	})
	return err
}

func GiftCredit(ctx context.Context, userID, adminID int, amount Micros, note string) error {
	if amount <= 0 {
		return fmt.Errorf("credit amount must be positive")
	}
	now := time.Now().UTC()
	_, err := applyLedger(ctx, userID, LedgerEntry{
		At:     now,
		Amount: amount,
		Kind:   LedgerGift,
		Ref:    fmt.Sprintf("admin:%d:%d", adminID, now.UnixNano()),
		Note:   note,
	})
	return err
}

func RecordPayment(ctx context.Context, userID, adminID int, amount Micros, note string) error {
	if amount <= 0 {
		return fmt.Errorf("payment amount must be positive")
	}
	now := time.Now().UTC()
	_, err := applyLedger(ctx, userID, LedgerEntry{
		At:     now,
		Amount: amount,
		Kind:   LedgerCredit,
		Ref:    fmt.Sprintf("manual:%d:%d", adminID, now.UnixNano()),
		Note:   note,
	})
	return err
}

func DeleteUser(ctx context.Context, userID int) error {
	res, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
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
