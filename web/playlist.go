package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/guregu/intertube/tube"
)

func createPlaylistForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	var pl tube.Playlist
	if idStr := r.PathValue("id"); idStr != "" {
		if pid, err := strconv.Atoi(idStr); err == nil && pid > 0 {
			if existing, err := u.GetPlaylist(ctx, pid); err == nil {
				pl = existing
			}
		}
	}

	var tracks []tube.Track
	query := r.FormValue("q")
	if query == "" && pl.Dynamic && pl.Query != "" {
		query = pl.Query
	}
	if query != "" {
		tracks, _ = lib.Query(query)
	} else if len(pl.Tracks) > 0 {
		tracks = lib.TracksByID(pl.Tracks)
	}

	data := struct {
		Tracks   []tube.Track
		Query    string
		Playlist tube.Playlist
	}{
		Tracks:   tracks,
		Query:    query,
		Playlist: pl,
	}

	page := "playlist"
	if r.FormValue("frag") == "tracks" {
		page = "playlist_tracks"
	}

	renderTemplate(ctx, w, page, data, http.StatusOK)
}

type PlaylistRequest struct {
	Meta struct {
		Name      string
		SortBy    string
		SortOrder string
	}
	Form struct {
		All  []PlaylistCond
		Expr string
	}
}

type PlaylistCond struct {
	Include bool   `json:"inc"`
	Attr    string `json:"attr"`
	Op      string `json:"op"`
	Val     string `json:"val"`
}

type PLUIMeta struct {
	Conds []PlaylistCond
	Ver   int
}

func createPlaylist(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	const playlistVersion = 1

	u, _ := userFrom(ctx)
	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	var plr PlaylistRequest
	if err := json.NewDecoder(r.Body).Decode(&plr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	expr := plr.Form.Expr

	tracks, err := lib.Query(expr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	enc, err := json.Marshal(PLUIMeta{
		Conds: plr.Form.All,
		Ver:   playlistVersion,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	var pid int
	if idStr != "" {
		pid, _ = strconv.Atoi(idStr)
	}

	if pid > 0 {
		existing, err := u.GetPlaylist(ctx, pid)
		if err != nil {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		existing.Name = plr.Meta.Name
		existing.Dynamic = true
		existing.Query = expr
		existing.With(tracks)
		existing.UIMeta = enc
		if err := u.SavePlaylist(ctx, &existing); err != nil {
			panic(err)
		}
		w.Header().Set("Location", "/music/playlists")
		w.WriteHeader(http.StatusOK)
		return
	}

	pl := tube.Playlist{
		UserID: u.ID,
		Name:   plr.Meta.Name,

		Dynamic: true,
		Query:   expr,
		UIMeta:  enc,
	}
	pl.With(tracks)

	if err := u.CreatePlaylist(ctx, &pl); err != nil {
		panic(err)
	}
	w.Header().Set("Location", "/music/playlists")
	w.WriteHeader(http.StatusCreated)
}

func deletePlaylist(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	pid, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := u.DeletePlaylist(ctx, pid); err != nil {
		if err == tube.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		panic(err)
	}
	w.WriteHeader(http.StatusOK)
}

func playlistTracks(lib *Library, pl tube.Playlist) ([]tube.Track, error) {
	var tracks []tube.Track
	var err error
	if pl.Dynamic {
		tracks, err = lib.Query(pl.Query)
	} else {
		tracks = lib.TracksByID(pl.Tracks)
	}
	return tracks, err
}
