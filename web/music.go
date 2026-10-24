package web

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"github.com/guregu/intertube/tube"
)

const tracksPerPage = 1000

type PlaylistView struct {
	Playlist tube.Playlist
	Tracks   []tube.Track
}

func showMusicHead(ctx context.Context, w http.ResponseWriter, r *http.Request) {

}

func showMusic(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	lastMod := lastestMod(u.LastMod)

	start := r.URL.Query().Get("next")

	kind := r.PathValue("kind")
	if kind == "" {
		kind = "all"
	}
	view := "music_" + kind
	inline := r.URL.Query().Get("render") == "inline"

	lib, err := getLibrary(ctx, u)
	if err != nil && err != tube.ErrNotFound {
		panic(err)
	}
	offset, _ := strconv.Atoi(start)
	filter := organize{
		size:   tracksPerPage,
		offset: offset,
	}
	tracks := lib.Tracks(filter)
	var nextID int
	if len(tracks) == filter.size {
		nextID = filter.offset + filter.size
		w.Header().Set("Tube-Next", strconv.Itoa(nextID))
	}
	w.Header().Set("Tube-Count", strconv.Itoa(len(tracks)))

	sortParam := r.URL.Query().Get("sort")
	reverse := false
	if len(sortParam) > 0 {
		if sortParam[0] == '-' {
			sortParam = sortParam[1:]
			reverse = true
		}
	} else {
		sortParam = "artist"
	}

	var playlistViews []PlaylistView
	if kind == "playlists" {
		if rawPlaylists, err := u.GetPlaylists(ctx); err == nil {
			for _, pl := range rawPlaylists {
				plTracks, _ := playlistTracks(lib, pl)
				playlistViews = append(playlistViews, PlaylistView{
					Playlist: pl,
					Tracks:   plTracks,
				})
			}
		}
	}

	data := struct {
		User      tube.User
		Tracks    []tube.Track
		Albums    [][]tube.Track
		Playlists []PlaylistView
		Sort      string
		Reverse   bool
		View      string
		ViewTmpl  string
		Next      string
		LastMod   int64
	}{
		User:      u,
		Tracks:    tracks,
		Albums:    byAlbum(tracks),
		Playlists: playlistViews,
		Sort:      sortParam,
		Reverse:   reverse,
		View:      view,
		ViewTmpl:  view + ".gohtml",
		LastMod:   lastMod.UnixNano(),
	}
	if nextID > 0 {
		data.Next = strconv.Itoa(nextID)
	}

	if inline {
		renderTemplate(ctx, w, view, data, http.StatusOK)
		return
	}

	renderTemplate(ctx, w, "music", data, http.StatusOK)
}

func byAlbum(tracks []tube.Track) [][]tube.Track {
	var albums [][]tube.Track
	var album []tube.Track
	for i, t := range tracks {
		if i != 0 && (!tracks[i-1].AlbumEqual(t)) {
			albums = append(albums, album)
			album = nil
		}
		album = append(album, t)
	}
	if len(album) > 0 {
		albums = append(albums, album)
	}
	for i, a := range albums {
		if len(a) == 0 || a[0].Picture.ID != "" {
			continue
		}
		for _, t := range a[1:] {
			if t.Picture.ID != "" {
				albums[i][0].Picture = t.Picture
				break
			}
		}
	}
	return albums
}

func sortTracks(tracks []tube.Track) {
	sort.Slice(tracks, func(i, j int) bool {
		a := tracks[i]
		b := tracks[j]
		if !a.ArtistEqual(b) {
			if a.AlbumArtist < b.AlbumArtist {
				return true
			}
			if a.AlbumArtist > b.AlbumArtist {
				return false
			}
			if a.Artist < b.Artist {
				return true
			}
			if a.Artist > b.Artist {
				return false
			}
		}
		if !a.AlbumEqual(b) {
			if a.Album < b.Album {
				return true
			}
			if a.Album > b.Album {
				return false
			}
		}
		if a.Disc != 0 && b.Disc != 0 {
			if a.Disc < b.Disc {
				return true
			}
			if a.Disc > b.Disc {
				return false
			}
		}
		if a.Number < b.Number {
			return true
		}
		if a.Number > b.Number {
			return false
		}
		if a.Title < b.Title {
			return true
		}
		if a.Title > b.Title {
			return false
		}
		if a.Year < b.Year {
			return true
		}
		if a.Year > b.Year {
			return false
		}
		if a.Filename < b.Filename {
			return true
		}
		if !a.Date.IsZero() && !b.Date.IsZero() {
			return a.Date.Before(b.Date)
		}
		return a.ID < b.ID
	})
}

func invert(n int) int {
	return -n
}
