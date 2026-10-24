package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/guregu/intertube/tube"
)

func createAppPassword(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	if !u.ValidPassword(r.FormValue("password")) {
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: "wrong password"}, http.StatusBadRequest)
		return
	}
	ap, secret, err := u.CreateAppPassword(ctx, r.FormValue("name"), r.FormValue("mode") != "standard")
	if err != nil {
		if !errors.Is(err, tube.ErrTooManyAppPasswords) {
			log.Println("create app password for user", u.ID, err)
		}
		renderSettings(ctx, w, settingsFormData{User: u, ErrorMsg: err.Error()}, http.StatusBadRequest)
		return
	}
	renderSettings(ctx, w, settingsFormData{User: u, NewAppPass: secret, NewAppName: ap.Name}, http.StatusOK)
}

func revokeAppPassword(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := tube.RevokeAppPassword(ctx, u.ID, id); err == tube.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		panic(err)
	}
	http.Redirect(w, r, "/settings#apps", http.StatusSeeOther)
}
