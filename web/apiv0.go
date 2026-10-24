package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/guregu/intertube/tube"
)

func init() {
	handle("POST /api/v0/login", loginV0)
	handle("GET /api/v0/tracks", listTracksV0)
	handle("GET /api/v0/tracks/{$}", listTracksV0)
}

func loginV0(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string
		Password string
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		panic(err)
	}

	email := req.Email
	pass := req.Password

	user, err := tube.GetUserByEmail(ctx, email)
	if err != nil || user.Unlock(ctx, pass) != nil {
		renderJSON(w, struct{ Error string }{"wrong e-mail or password"}, http.StatusUnauthorized)
		return
	}

	sesh, err := tube.CreateSession(ctx, user)
	if err != nil {
		panic(err)
	}

	http.SetCookie(w, validAuthCookie(sesh))

	data := struct {
		Session string
	}{
		Session: sesh.Token,
	}

	renderJSON(w, data, http.StatusOK)
}

func listTracksV0(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	data := struct {
		Tracks tube.Tracks
		Next   string
	}{}

	tracks, next, err := u.GetTracksPartial(ctx, 500, r.URL.Query().Get("start"))
	if err != nil {
		panic(err)
	}
	data.Tracks = tracks
	for i, t := range data.Tracks {
		t.DL = t.FileURL()
		t.Lyrics = nil
		data.Tracks[i] = t
	}
	data.Next = next

	renderJSON(w, data, http.StatusOK)
}
