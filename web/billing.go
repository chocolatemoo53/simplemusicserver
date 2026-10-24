package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

const errStorageFull = "Not enough balance to store this. Add funds or delete some files."

const errVerifyEmail = "Confirm your e-mail address to use the free storage. Check your inbox, or resend the link from settings."

const (
	noticeLapsed = iota + 1
	noticeLockSoon
	noticeDeleteSoon
)

func StartBilling(ctx context.Context) {
	go func() {
		runBilling(ctx, time.Now().UTC())
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				runBilling(ctx, now.UTC())
			}
		}
	}()
}

func runBilling(ctx context.Context, now time.Time) {
	runRetention(ctx, now)

	stale, err := tube.StalePendingRefunds(ctx, now.Add(-time.Hour))
	if err != nil {
		log.Println("billing: stale refunds:", err)
	}
	for _, r := range stale {
		log.Printf("billing: refund %d for user %d (%v, invoice %s) has been pending since %s; check BTCPay for a pull payment named \"Refund %d\" before resolving it",
			r.ID, r.UserID, r.Amount, r.InvoiceID, r.At.Format(time.RFC3339), r.ID)
	}

	users, err := tube.GetAllUsers(ctx)
	if err != nil {
		log.Println("billing: list users:", err)
		return
	}
	for _, u := range users {
		if err := billUser(ctx, u, now); err != nil {
			log.Println("billing: user", u.ID, err)
		}
	}
}

func billUser(ctx context.Context, u tube.User, now time.Time) error {
	if _, err := tube.ChargeDay(ctx, u, now); err != nil {
		return err
	}
	u, err := tube.GetUser(ctx, u.ID)
	if err != nil {
		return err
	}
	if stage := noticeStage(u, now); stage > u.Notified {
		if err := sendNotice(u, stage, now); err != nil {
			log.Println("billing: notice for user", u.ID, err)
		}
		if err := tube.SetNotified(ctx, u.ID, stage, now); err != nil {
			return err
		}
		u.Notified, u.NotifiedAt = stage, now
	}
	if readyForCleanup(u, now) {
		return deleteLibrary(ctx, u)
	}
	return nil
}

func noticeStage(u tube.User, now time.Time) int {
	if u.Exempt || u.LapsedAt.IsZero() {
		return 0
	}
	lapsed := now.Sub(u.LapsedAt)
	switch {
	case lapsed >= daysDur(tube.Prices.LockDays-tube.Prices.DeleteWarnDays):
		return noticeDeleteSoon
	case lapsed >= daysDur(tube.Prices.GraceDays-tube.Prices.LockWarningDays):
		return noticeLockSoon
	default:
		return noticeLapsed
	}
}

func readyForCleanup(u tube.User, now time.Time) bool {
	return u.State(now) == tube.StateCleanup &&
		u.Notified >= noticeDeleteSoon &&
		now.Sub(u.NotifiedAt) >= daysDur(tube.Prices.DeleteWarnDays)
}

func deleteLibrary(ctx context.Context, u tube.User) error {
	objects, err := tube.UserObjects(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, key := range objects {
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
	}
	if err := tube.WipeLibrary(ctx, u.ID); err != nil {
		return err
	}
	log.Printf("billing: deleted %d stored objects for user %d", len(objects), u.ID)
	return nil
}

func sendNotice(u tube.User, stage int, now time.Time) error {
	if !mailEnabled() {
		return nil
	}
	topUp := fmt.Sprintf("https://%s/buy/", Domain)
	lockAt := u.LapsedAt.Add(daysDur(tube.Prices.GraceDays))
	deleteAt := u.LapsedAt.Add(daysDur(tube.Prices.LockDays))
	if earliest := now.Add(daysDur(tube.Prices.DeleteWarnDays)); deleteAt.Before(earliest) {
		deleteAt = earliest
	}

	var subject, body string
	switch stage {
	case noticeLapsed:
		subject = fmt.Sprintf("Your %s balance has run out", Domain)
		body = fmt.Sprintf("Your balance at %s has run out. Your music stays playable until %s.\n\n"+
			"Add funds to keep everything as it is:\n%s\n", Domain, lockAt.Format(time.DateOnly), topUp)
	case noticeLockSoon:
		subject = fmt.Sprintf("Playback on %s pauses on %s", Domain, lockAt.Format(time.DateOnly))
		body = fmt.Sprintf("Your balance at %s is still empty. On %s playback pauses until you add funds. You can still log in and download your files.\n\n"+
			"Add funds:\n%s\n", Domain, lockAt.Format(time.DateOnly), topUp)
	case noticeDeleteSoon:
		subject = fmt.Sprintf("Your %s library will be deleted on %s", Domain, deleteAt.Format(time.DateOnly))
		body = fmt.Sprintf("Your balance at %s has been empty since %s. On %s your uploaded music will be deleted. This can't be undone.\n\n"+
			"Download your files or add funds before then:\n%s\n", Domain, u.LapsedAt.Format(time.DateOnly), deleteAt.Format(time.DateOnly), topUp)
	default:
		return fmt.Errorf("unknown notice stage %d", stage)
	}
	return sendMail(u, subject, body)
}

func requireStanding(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	u, ok := userFrom(ctx)
	if !ok || !u.Locked(time.Now()) {
		return ctx
	}
	if isSubsonicReq(r) {
		writeSubsonic(ctx, w, r, subErr(50, "playback is paused: add funds at https://"+Domain+"/buy/"))
		return nil
	}
	http.Redirect(w, r, "/buy/", http.StatusSeeOther)
	return nil
}

func daysDur(n int) time.Duration {
	return time.Duration(n) * 24 * time.Hour
}

func runRetention(ctx context.Context, now time.Time) {
	if _, err := tube.DeleteStaleFiles(ctx, now.Add(-24*time.Hour)); err != nil {
		log.Println("retention: abandoned uploads:", err)
	}
	if _, err := tube.DeleteExpiredSessions(ctx, now); err != nil {
		log.Println("retention: expired sessions:", err)
	}
	if _, err := tube.PruneCharges(ctx, now.Add(-tube.ChargeHistory)); err != nil {
		log.Println("retention: old charges:", err)
	}
}
