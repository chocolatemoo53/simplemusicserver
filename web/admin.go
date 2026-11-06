package web

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

var adminNotices = map[string]string{
	"deleted": "Account deleted",
}

func adminIndex(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	users, err := tube.GetAllUsers(ctx)
	if err != nil {
		panic(err)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].LastMod.After(users[j].LastMod)
	})

	data := struct {
		Users      []tube.User
		Totals     tube.Totals
		Notice     string
		Replicated bool
		Replicas   tube.ReplicaStats
	}{
		Users:      users,
		Totals:     tube.SumUsers(users),
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
	admin, _ := userFrom(ctx)
	data := struct {
		Target   tube.User
		IsSelf   bool
		Notice   string
		ErrorMsg string
	}{
		Target:   u,
		IsSelf:   admin.ID == u.ID,
		Notice:   notice,
		ErrorMsg: errMsg,
	}
	renderTemplate(ctx, w, "admin-user", data, code)
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
	log.Printf("deleted %d stored objects for user %d", len(objects), u.ID)
	return nil
}

func deleteAccount(ctx context.Context, u tube.User) error {
	if err := deleteLibrary(ctx, u); err != nil {
		return err
	}
	return tube.DeleteUser(ctx, u.ID)
}
