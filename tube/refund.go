package tube

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RefundStatus string

const (
	RefundPending RefundStatus = "pending"
	RefundCreated RefundStatus = "created"
	RefundFailed  RefundStatus = "failed"
)

const RefundCooldown = 24 * time.Hour

var (
	ErrRefundTooLarge = errors.New("that's more than your refundable balance")
	ErrRefundTooSoon  = errors.New("you can request one refund per day")
)

type Refund struct {
	ID        int
	UserID    int
	InvoiceID string
	Amount    Micros
	Status    RefundStatus
	Link      string
	At        time.Time
}

type refundSource struct {
	invoiceID string
	remaining Micros
}

const btcpayRefPrefix = "btcpay:"

func refundSources(ctx context.Context, q querier, userID int, now time.Time) ([]refundSource, error) {
	since := now.Add(-days(Prices.RefundDays)).UnixNano()
	rows, err := q.QueryContext(ctx,
		`SELECT l.ref, l.amount - COALESCE((
			SELECT SUM(r.amount) FROM refunds r
			WHERE r.user_id = l.user_id AND r.invoice_id = substr(l.ref, ?) AND r.status != ?
		), 0)
		FROM ledger l
		WHERE l.user_id = ? AND l.kind = ? AND l.ref LIKE ? AND l.at >= ?
		ORDER BY l.at DESC, l.id DESC`,
		len(btcpayRefPrefix)+1, string(RefundFailed), userID, string(LedgerCredit), btcpayRefPrefix+"%", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []refundSource
	for rows.Next() {
		var (
			ref       string
			remaining Micros
		)
		if err := rows.Scan(&ref, &remaining); err != nil {
			return nil, err
		}
		if remaining > 0 {
			sources = append(sources, refundSource{invoiceID: strings.TrimPrefix(ref, btcpayRefPrefix), remaining: remaining})
		}
	}
	return sources, rows.Err()
}

func refundable(u User, sources []refundSource) Micros {
	var total Micros
	for _, s := range sources {
		total += s.remaining
	}
	return max(0, min(total, u.Balance))
}

func Refundable(ctx context.Context, u User, now time.Time) (Micros, error) {
	sources, err := refundSources(ctx, db, u.ID, now)
	if err != nil {
		return 0, err
	}
	return refundable(u, sources), nil
}

func RequestRefund(ctx context.Context, userID int, amount Micros, now time.Time) ([]Refund, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("refund amount must be positive")
	}
	var parts []Refund
	err := inTx(ctx, func(tx *sql.Tx) error {
		u, err := getUser(ctx, tx, "id = ?", userID)
		if err != nil {
			return err
		}
		var last int64
		err = tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(at), 0) FROM refunds WHERE user_id = ? AND status != ?`,
			userID, string(RefundFailed)).Scan(&last)
		if err != nil {
			return err
		}
		if last != 0 && now.Sub(time.Unix(0, last)) < RefundCooldown {
			return ErrRefundTooSoon
		}
		sources, err := refundSources(ctx, tx, userID, now)
		if err != nil {
			return err
		}
		if amount > refundable(u, sources) {
			return ErrRefundTooLarge
		}

		remaining := amount
		for _, s := range sources {
			if remaining == 0 {
				break
			}
			part, err := insertRefund(ctx, tx, userID, s.invoiceID, min(s.remaining, remaining), now)
			if err != nil {
				return err
			}
			parts = append(parts, part)
			remaining -= part.Amount
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parts, nil
}

func insertRefund(ctx context.Context, tx *sql.Tx, userID int, invoiceID string, amount Micros, now time.Time) (Refund, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO refunds (user_id, invoice_id, amount, status, at) VALUES (?, ?, ?, ?, ?)`,
		userID, invoiceID, int64(amount), string(RefundPending), now.UnixNano())
	if err != nil {
		return Refund{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Refund{}, err
	}
	entry := LedgerEntry{At: now, Amount: -amount, Kind: LedgerRefund, Ref: fmt.Sprintf("refund:%d", id)}
	if _, err := applyLedgerTx(ctx, tx, userID, entry); err != nil {
		return Refund{}, err
	}
	return Refund{ID: int(id), UserID: userID, InvoiceID: invoiceID, Amount: amount, Status: RefundPending, At: now}, nil
}

func CompleteRefund(ctx context.Context, id int, pullPaymentID, link string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE refunds SET status = ?, pull_payment_id = ?, link = ? WHERE id = ? AND status = ?`,
		string(RefundCreated), pullPaymentID, link, id, string(RefundPending))
	return err
}

func FailRefund(ctx context.Context, id int) error {
	return inTx(ctx, func(tx *sql.Tx) error {
		var (
			userID int
			amount Micros
		)
		err := tx.QueryRowContext(ctx,
			`SELECT user_id, amount FROM refunds WHERE id = ? AND status = ?`,
			id, string(RefundPending)).Scan(&userID, &amount)
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE refunds SET status = ? WHERE id = ?`, string(RefundFailed), id); err != nil {
			return err
		}
		entry := LedgerEntry{At: time.Now().UTC(), Amount: amount, Kind: LedgerRefundCancelled, Ref: fmt.Sprintf("refund-cancelled:%d", id)}
		_, err = applyLedgerTx(ctx, tx, userID, entry)
		return err
	})
}

func GetRefunds(ctx context.Context, userID int, limit int) ([]Refund, error) {
	return queryRefunds(ctx,
		`SELECT id, user_id, invoice_id, amount, status, link, at FROM refunds WHERE user_id = ? ORDER BY at DESC, id DESC LIMIT ?`,
		userID, limit)
}

func StalePendingRefunds(ctx context.Context, olderThan time.Time) ([]Refund, error) {
	return queryRefunds(ctx,
		`SELECT id, user_id, invoice_id, amount, status, link, at FROM refunds WHERE status = ? AND at < ? ORDER BY at`,
		string(RefundPending), olderThan.UnixNano())
}

func queryRefunds(ctx context.Context, query string, args ...any) ([]Refund, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refunds []Refund
	for rows.Next() {
		var (
			r  Refund
			at int64
		)
		if err := rows.Scan(&r.ID, &r.UserID, &r.InvoiceID, &r.Amount, &r.Status, &r.Link, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(0, at).UTC()
		refunds = append(refunds, r)
	}
	return refunds, rows.Err()
}
