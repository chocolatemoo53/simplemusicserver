package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/intertube/tube"
)

func deleteTrack(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	trackID := r.PathValue("id")

	object, err := tube.RemoveTrack(ctx, u.ID, trackID)
	if err == tube.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		panic(err)
	}
	deleteObjects(ctx, u.ID, []string{object})
	if err := u.UpdateLastMod(ctx); err != nil {
		panic(err)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "deleted "+trackID)
}

func incPlays(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	trackID := r.PathValue("id")
	secs, _ := strconv.ParseFloat(r.FormValue("duration"), 64)

	track, err := u.GetTrack(ctx, trackID)
	if err != nil {
		panic(err)
	}

	if track.Duration == 0 && secs > 0 {
		if _, err := u.SetDuration(ctx, trackID, int(secs)); err != nil && !errors.Is(err, tube.ErrConflict) {
			panic(err)
		}
	}

	track, err = u.IncPlays(ctx, trackID)
	if err != nil {
		panic(err)
	}

	if err := tube.IncTotalPlays(ctx, int(secs)); err != nil {
		panic(err)
	}

	fmt.Fprint(w, track.Plays)
}

func setResume(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	trackID := r.PathValue("id")
	secs, _ := strconv.ParseFloat(r.FormValue("cur"), 64)
	at, _ := strconv.ParseInt(r.FormValue("time"), 10, 64)
	mod := msec2time(at)

	if _, err := u.SetResume(ctx, trackID, secs, mod); err != nil {
		if err == tube.ErrNotFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, tube.ErrConflict) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		panic(err)
	}

	if err := u.UpdateLastMod(ctx); err != nil {
		panic(err)
	}

	w.WriteHeader(http.StatusNoContent)
}

func editTrackForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	id := r.PathValue("id")
	ids := strings.Split(id, ",")
	t, tracks, err := getMultiTracks(ctx, u, ids)
	if err != nil {
		panic(err)
	}
	multi := len(tracks) > 1

	data := struct {
		User     tube.User
		IDs      string
		Track    tube.Track
		Tracks   tube.Tracks
		Multi    bool
		LastMod  int64
		ErrorMsg string
	}{
		User:    u,
		IDs:     id,
		Track:   t,
		Tracks:  tracks,
		Multi:   multi,
		LastMod: u.LastMod.UnixNano(),
	}
	renderTemplate(ctx, w, "track-edit", data, http.StatusOK)
}

func editTrack(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	id := r.PathValue("id")
	ids := strings.Split(id, ",")
	t, tracks, err := getMultiTracks(ctx, u, ids)
	if err != nil {
		panic(err)
	}
	multi := len(tracks) > 1

	renderError := func(err error) {
		var data = struct {
			User     tube.User
			IDs      string
			Track    tube.Track
			Tracks   tube.Tracks
			Multi    bool
			LastMod  int64
			ErrorMsg string
		}{
			User:     u,
			IDs:      id,
			Track:    t,
			Tracks:   tracks,
			Multi:    multi,
			LastMod:  u.LastMod.UnixNano(),
			ErrorMsg: err.Error(),
		}
		renderTemplate(ctx, w, "track-edit", data, http.StatusOK)
	}

	newPic := t.Picture
	picdel := false
	if r.FormValue("picdel") == "on" {
		newPic = tube.Picture{}
		picdel = true
	} else {
		f, fh, err := r.FormFile("pic")
		if err != http.ErrMissingFile && err != nil {
			renderError(err)
			return
		}
		if err == nil {
			data, err := io.ReadAll(f)
			if err != nil {
				renderError(err)
				return
			}
			ext := path.Ext(fh.Filename)
			if len(ext) > 0 && ext[0] == '.' {
				ext = ext[1:]
			}
			pic, err := savePicture(ctx, u, data, ext, fh.Header.Get("Content-Type"), t.Picture.Desc)
			if err != nil {
				renderError(err)
				return
			}
			newPic = pic
		}
	}

	if !multi {
		info := t.Info
		info.Title = r.FormValue("title")
		info.Artist = r.FormValue("artist")
		info.Album = r.FormValue("album")
		info.AlbumArtist = r.FormValue("albumartist")
		info.Composer = r.FormValue("composer")
		info.Comment = r.FormValue("comment")
		t.ApplyInfo(info)
		t.Year, _ = strconv.Atoi(r.FormValue("year"))
		t.Number, _ = strconv.Atoi(r.FormValue("number"))
		t.Total, _ = strconv.Atoi(r.FormValue("total"))
		t.Disc, _ = strconv.Atoi(r.FormValue("disc"))
		t.Discs, _ = strconv.Atoi(r.FormValue("discs"))
		t.Picture = newPic
		t.Tags = strings.Split(strings.ToLower(r.FormValue("tags")), " ")
		t.Dirty = true
		t.LastMod = time.Now().UTC()
		if err := u.SaveTrack(ctx, &t); err != nil {
			renderError(err)
			return
		}
		if err := u.UpdateLastMod(ctx); err != nil {
			renderError(err)
			return
		}

		http.Redirect(w, r, "/track/"+t.ID+"/edit", http.StatusSeeOther)
		return
	}

	info := tube.TrackInfo{
		Artist:      r.FormValue("artist"),
		Album:       r.FormValue("album"),
		AlbumArtist: r.FormValue("albumartist"),
		Composer:    r.FormValue("composer"),
		Comment:     r.FormValue("comment"),
	}
	year, _ := strconv.Atoi(r.FormValue("year"))
	disc, _ := strconv.Atoi(r.FormValue("disc"))
	discs, _ := strconv.Atoi(r.FormValue("discs"))

	update := func(t *tube.Track) {
		if info.Artist != "" {
			t.Artist = strings.ToLower(info.Artist)
			t.Info.Artist = info.Artist
		}
		if info.Album != "" {
			t.Album = strings.ToLower(info.Album)
			t.Info.Album = info.Album
		}
		if info.AlbumArtist != "" {
			t.AlbumArtist = strings.ToLower(info.AlbumArtist)
			t.Info.AlbumArtist = info.AlbumArtist
		}
		if info.Composer != "" {
			t.Composer = strings.ToLower(info.Composer)
			t.Info.Composer = info.Composer
		}
		if info.Comment != "" {
			t.Comment = strings.ToLower(info.Comment)
			t.Info.Comment = info.Comment
		}
		if year != 0 {
			t.Year = year
		}
		if disc != 0 {
			t.Disc = disc
		}
		if discs != 0 {
			t.Discs = discs
		}
		if newPic != (tube.Picture{}) || picdel {
			t.Picture = newPic
		}
	}

	if err := u.MassUpdateTracks(ctx, ids, update); err != nil {
		renderError(err)
		return
	}
	if err := u.UpdateLastMod(ctx); err != nil {
		renderError(err)
		return
	}
	http.Redirect(w, r, "/track/"+id+"/edit", http.StatusSeeOther)
}

func getMultiTracks(ctx context.Context, u tube.User, ids []string) (t tube.Track, tracks tube.Tracks, err error) {
	multi := len(ids) > 1
	if !multi {
		t, err = u.GetTrack(ctx, ids[0])
		if err != nil {
			panic(err)
		}
		tracks = tube.Tracks{t}
	} else {
		tracks, err = u.GetTracksBatch(ctx, ids)
		if err != nil {
			panic(err)
		}
		t = tracks[0]
		for _, tx := range tracks {
			if tx.Artist != t.Artist {
				t.Artist = ""
				t.Info.Artist = ""
			}
			if tx.Album != t.Album {
				t.Album = ""
				t.Info.Album = ""
			}
			if tx.AlbumArtist != t.AlbumArtist {
				t.AlbumArtist = ""
				t.Info.AlbumArtist = ""
			}
			if tx.Composer != t.Composer {
				t.Composer = ""
				t.Info.Composer = ""
			}
			if tx.Year != t.Year {
				t.Year = 0
			}
			if tx.Disc != t.Disc {
				t.Disc = 0
			}
			if tx.Discs != t.Discs {
				t.Discs = 0
			}
			if tx.Comment != t.Comment {
				t.Comment = ""
				t.Info.Comment = ""
			}
		}
		t.Picture = sharedPicture(tracks)
	}
	return
}

func sharedPicture(tracks tube.Tracks) tube.Picture {
	var pic tube.Picture
	for _, t := range tracks {
		if t.Picture.ID == "" {
			continue
		}
		if pic.ID != "" && t.Picture != pic {
			return tube.Picture{}
		}
		pic = t.Picture
	}
	return pic
}

func msec2time(msecs int64) time.Time {
	t := time.Unix(msecs/1000, (msecs%1000)*1000000)
	return t
}
