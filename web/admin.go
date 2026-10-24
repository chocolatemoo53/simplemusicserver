package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

const adminHistory = 100

var adminNotices = map[string]string{
	"credit":   "Credit added",
	"exempt":   "Exempt status updated",
	"deleted":  "Account deleted",
	"unexempt": "Exempt status removed",
}

func adminIndex(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	users, err := tube.GetAllUsers(ctx)
	if err != nil {
		panic(err)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].LastMod.After(users[j].LastMod)
	})
	stale, err := tube.StalePendingRefunds(ctx, now.Add(-time.Hour))
	if err != nil {
		panic(err)
	}

	data := struct {
		Users      []tube.User
		Totals     tube.Totals
		Stale      []tube.Refund
		Notice     string
		Replicated bool
		Replicas   tube.ReplicaStats
	}{
		Users:      users,
		Totals:     tube.SumUsers(users, now),
		Stale:      stale,
		Notice:     adminNotices[r.URL.Query().Get("done")],
		Replicated: storage.Replicated(),
	}
	if data.Replicated {
		if data.Replicas, err = tube.GetReplicaStats(ctx); err != nil {
			panic(err)
		}
	}
	renderTemplate(ctx, w, "admin", data, http.StatusOK)
}

func adminTarget(ctx context.Context, w http.ResponseWriter, r *http.Request) (tube.User, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return tube.User{}, false
	}
	u, err := tube.GetUser(ctx, id)
	if err == tube.ErrNotFound {
		http.NotFound(w, r)
		return tube.User{}, false
	}
	if err != nil {
		panic(err)
	}
	return u, true
}

func adminUser(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := adminTarget(ctx, w, r)
	if !ok {
		return
	}
	renderAdminUser(ctx, w, u, adminNotices[r.URL.Query().Get("done")], "", http.StatusOK)
}

func renderAdminUser(ctx context.Context, w http.ResponseWriter, u tube.User, notice, errMsg string, code int) {
	ledger, err := tube.GetLedger(ctx, u.ID, adminHistory)
	if err != nil {
		panic(err)
	}
	refunds, err := tube.GetRefunds(ctx, u.ID, adminHistory)
	if err != nil {
		panic(err)
	}
	admin, _ := userFrom(ctx)
	data := struct {
		Target   tube.User
		Ledger   []tube.LedgerEntry
		Refunds  []tube.Refund
		IsSelf   bool
		Notice   string
		ErrorMsg string
	}{
		Target:   u,
		Ledger:   ledger,
		Refunds:  refunds,
		IsSelf:   admin.ID == u.ID,
		Notice:   notice,
		ErrorMsg: errMsg,
	}
	renderTemplate(ctx, w, "admin-user", data, code)
}

func adminRedirect(w http.ResponseWriter, r *http.Request, u tube.User, done string) {
	http.Redirect(w, r, fmt.Sprintf("/admin/user/%d?done=%s", u.ID, done), http.StatusSeeOther)
}

func adminCredit(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := adminTarget(ctx, w, r)
	if !ok {
		return
	}
	amount, err := parseUSD(r.FormValue("amount"))
	if err != nil {
		renderAdminUser(ctx, w, u, "", err.Error(), http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 200 {
		renderAdminUser(ctx, w, u, "", "note is too long (200 characters max)", http.StatusBadRequest)
		return
	}
	admin, _ := userFrom(ctx)
	record, kind := tube.GiftCredit, "gift"
	if r.FormValue("kind") == "payment" {
		record, kind = tube.RecordPayment, "payment"
	}
	if err := record(ctx, u.ID, admin.ID, amount, note); err != nil {
		panic(err)
	}
	log.Printf("admin %d credited user %d with %v (%s)", admin.ID, u.ID, amount, kind)
	adminRedirect(w, r, u, "credit")
}

func adminExempt(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := adminTarget(ctx, w, r)
	if !ok {
		return
	}
	exempt := r.FormValue("exempt") == "1"
	if err := tube.SetExempt(ctx, u.ID, exempt); err != nil {
		panic(err)
	}
	admin, _ := userFrom(ctx)
	log.Printf("admin %d set exempt=%v for user %d", admin.ID, exempt, u.ID)
	done := "exempt"
	if !exempt {
		done = "unexempt"
	}
	adminRedirect(w, r, u, done)
}

func adminDelete(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := adminTarget(ctx, w, r)
	if !ok {
		return
	}
	admin, _ := userFrom(ctx)
	if admin.ID == u.ID {
		renderAdminUser(ctx, w, u, "", "you can't delete your own account from here", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), u.Email) {
		renderAdminUser(ctx, w, u, "", "type the account's e-mail address exactly to confirm deletion", http.StatusBadRequest)
		return
	}
	if err := deleteAccount(ctx, u); err != nil {
		log.Printf("admin %d deleting user %d: %v", admin.ID, u.ID, err)
		renderAdminUser(ctx, w, u, "", "deletion stopped partway: "+err.Error()+". some files may already be gone; try again to finish.", http.StatusInternalServerError)
		return
	}
	log.Printf("admin %d deleted user %d", admin.ID, u.ID)
	http.Redirect(w, r, "/admin/?done=deleted", http.StatusSeeOther)
}

func deleteAccount(ctx context.Context, u tube.User) error {
	if err := deleteLibrary(ctx, u); err != nil {
		return err
	}
	return tube.DeleteUser(ctx, u.ID)
}
