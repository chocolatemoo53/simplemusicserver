package web

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/guregu/intertube/tube"
)

func homepage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	data := struct {
		User tube.User
		Home libraryHome
	}{
		User: u,
	}
	if !u.Locked(time.Now()) {
		tracks, err := u.GetTracks(ctx)
		if err != nil {
			log.Println("homepage:", err)
		}
		data.Home = summarize(tracks)
	}
	renderTemplate(ctx, w, "index", data, http.StatusOK)
}

type libraryHome struct {
	Tracks  int
	Albums  int
	Artists int
	Recent  []tube.Track
	Played  []tube.Track
}

func summarize(tracks tube.Tracks) libraryHome {
	home := libraryHome{Tracks: len(tracks)}
	albums := make(map[string]bool)
	artists := make(map[string]bool)
	for _, t := range tracks {
		albums[t.AlbumCode()] = true
		if a := t.AnyArtist(); a != "" {
			artists[a] = true
		}
	}
	home.Albums = len(albums)
	home.Artists = len(artists)

	byDate := slices.Clone(tracks)
	slices.SortStableFunc(byDate, func(a, b tube.Track) int { return b.Date.Compare(a.Date) })
	seen := make(map[string]bool)
	for _, t := range byDate {
		if len(home.Recent) == 6 {
			break
		}
		if code := t.AlbumCode(); !seen[code] {
			seen[code] = true
			home.Recent = append(home.Recent, t)
		}
	}

	for _, t := range tracks {
		if !t.LastPlayed.IsZero() {
			home.Played = append(home.Played, t)
		}
	}
	slices.SortStableFunc(home.Played, func(a, b tube.Track) int { return b.LastPlayed.Compare(a.LastPlayed) })
	if len(home.Played) > 8 {
		home.Played = home.Played[:8]
	}
	return home
}

func subsonicHelp(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	data := struct {
		User tube.User
	}{
		User: u,
	}
	renderTemplate(ctx, w, "subsonic", data, http.StatusOK)
}

func serveFont(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != filepath.Base(name) || filepath.Ext(name) != ".woff2" {
		http.NotFound(w, r)
		return
	}
	dir, err := assetsDir()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(dir, "fonts", name))
}

func serveIcon(name string) handlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		dir, err := assetsDir()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, filepath.Join(dir, "icons", name))
	}
}

func privacyPolicy(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	renderTemplate(ctx, w, "privacy", nil, http.StatusOK)
}

func termsOfService(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	renderTemplate(ctx, w, "terms", nil, http.StatusOK)
}
