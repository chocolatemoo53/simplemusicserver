package tube

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Micros int64

const MicrosPerDollar Micros = 1_000_000

const (
	bytesPerMB   = 1_000_000
	mbPerGB      = 1_000
	daysPerMonth = 30
)

func (m Micros) String() string {
	sign := ""
	if m < 0 {
		sign = "-"
		m = -m
	}
	cents := (m + 5_000) / 10_000
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

type Pricing struct {
	FreeBytes       int64
	PerGBMonth      Micros
	GraceDays       int
	LockDays        int
	MinUploadDays   int
	LockWarningDays int
	DeleteWarnDays  int
	RefundDays      int
	RefundFeePct    float64
}

var Prices = Pricing{
	FreeBytes:       5 * 1_000_000_000,
	PerGBMonth:      20_000,
	GraceDays:       30,
	LockDays:        90,
	MinUploadDays:   7,
	LockWarningDays: 5,
	DeleteWarnDays:  10,
	RefundDays:      90,
}

func (p Pricing) DailyCost(usage int64) Micros {
	billable := usage - p.FreeBytes
	if billable <= 0 {
		return 0
	}
	mb := Micros(billable / bytesPerMB)
	return mb * p.PerGBMonth / (mbPerGB * daysPerMonth)
}

func (p Pricing) MonthlyCost(usage int64) Micros {
	return p.DailyCost(usage) * daysPerMonth
}

type AccountState string

const (
	StateActive  AccountState = "active"
	StateGrace   AccountState = "grace"
	StateLocked  AccountState = "locked"
	StateCleanup AccountState = "cleanup"
)

func (u User) State(now time.Time) AccountState {
	if u.Exempt || u.LapsedAt.IsZero() {
		return StateActive
	}
	lapsed := now.Sub(u.LapsedAt)
	switch {
	case lapsed < days(Prices.GraceDays):
		return StateGrace
	case lapsed < days(Prices.LockDays):
		return StateLocked
	default:
		return StateCleanup
	}
}

func (u User) Locked(now time.Time) bool {
	switch u.State(now) {
	case StateLocked, StateCleanup:
		return true
	}
	return false
}

func (u User) MonthlyCost() Micros {
	if u.Exempt {
		return 0
	}
	return Prices.MonthlyCost(u.Usage)
}

func (u User) PaidUntil(now time.Time) time.Time {
	daily := Prices.DailyCost(u.Usage)
	if daily == 0 || u.Exempt || u.Balance <= 0 {
		return time.Time{}
	}
	return now.Add(days(int(u.Balance / daily)))
}

func (u User) CanStore(extra int64, now time.Time) bool {
	if u.Exempt {
		return true
	}
	if u.NeedsVerification() && u.Balance <= 0 {
		return false
	}
	total := u.Usage + extra
	if total <= Prices.FreeBytes {
		return true
	}
	if u.State(now) != StateActive {
		return false
	}
	return u.Balance >= Prices.DailyCost(total)*Micros(Prices.MinUploadDays)
}

type LedgerKind string

const (
	LedgerCharge          LedgerKind = "charge"
	LedgerCredit          LedgerKind = "credit"
	LedgerRefund          LedgerKind = "refund"
	LedgerRefundCancelled LedgerKind = "refund_cancelled"
	LedgerGift            LedgerKind = "gift"
)

type LedgerEntry struct {
	At     time.Time
	Amount Micros
	Kind   LedgerKind
	Ref    string
	Note   string
}

func Credit(ctx context.Context, userID int, amount Micros, ref string) (applied bool, err error) {
	if amount <= 0 {
		return false, fmt.Errorf("credit amount must be positive: %d", amount)
	}
	return applyLedger(ctx, userID, LedgerEntry{At: time.Now().UTC(), Amount: amount, Kind: LedgerCredit, Ref: ref})
}

func ChargeDay(ctx context.Context, u User, day time.Time) (bool, error) {
	if u.Exempt {
		return false, nil
	}
	cost := Prices.DailyCost(u.Usage)
	if cost == 0 {
		return false, clearLapse(ctx, u)
	}
	ref := fmt.Sprintf("charge:%d:%s", u.ID, day.UTC().Format(time.DateOnly))
	return applyLedger(ctx, u.ID, LedgerEntry{At: day.UTC(), Amount: -cost, Kind: LedgerCharge, Ref: ref})
}

func applyLedger(ctx context.Context, userID int, e LedgerEntry) (bool, error) {
	applied := false
	err := inTx(ctx, func(tx *sql.Tx) error {
		var err error
		applied, err = applyLedgerTx(ctx, tx, userID, e)
		return err
	})
	return applied, err
}

func applyLedgerTx(ctx context.Context, tx *sql.Tx, userID int, e LedgerEntry) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO ledger (user_id, at, amount, kind, ref, note) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (ref) DO NOTHING`,
		userID, e.At.UnixNano(), int64(e.Amount), string(e.Kind), e.Ref, e.Note)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	_, err = updateUserTx(ctx, tx, userID, func(u *User) error {
		u.Balance += e.Amount
		updateLapse(u, e.At)
		return nil
	})
	return err == nil, err
}

func updateLapse(u *User, at time.Time) {
	switch {
	case u.Balance > 0 || Prices.DailyCost(u.Usage) == 0:
		u.LapsedAt = time.Time{}
		u.Notified = 0
		u.NotifiedAt = time.Time{}
	case u.LapsedAt.IsZero():
		u.LapsedAt = at
	}
}

func clearLapse(ctx context.Context, u User) error {
	if u.LapsedAt.IsZero() {
		return nil
	}
	_, err := updateUser(ctx, u.ID, func(u *User) error {
		updateLapse(u, time.Now().UTC())
		return nil
	})
	return err
}

func SetNotified(ctx context.Context, userID int, stage int, at time.Time) error {
	_, err := updateUser(ctx, userID, func(u *User) error {
		u.Notified = stage
		u.NotifiedAt = at
		return nil
	})
	return err
}

func GetLedger(ctx context.Context, userID int, limit int) ([]LedgerEntry, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT at, amount, kind, ref, note FROM ledger WHERE user_id = ? ORDER BY at DESC, id DESC LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var (
			e  LedgerEntry
			at int64
		)
		if err := rows.Scan(&at, &e.Amount, &e.Kind, &e.Ref, &e.Note); err != nil {
			return nil, err
		}
		e.At = time.Unix(0, at).UTC()
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func days(n int) time.Duration {
	return time.Duration(n) * 24 * time.Hour
}

const ChargeHistory = 90 * 24 * time.Hour

func PruneCharges(ctx context.Context, before time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM ledger WHERE kind = ? AND at < ?`, string(LedgerCharge), before.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
