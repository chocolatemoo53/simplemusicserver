package web

import (
	"context"
	"encoding/xml"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guregu/intertube/tube"
)

type subsonicSong struct {
	XMLName     xml.Name  `json:"-"`
	ID          tube.SSID `xml:"id,attr" json:"id"`
	Parent      string    `xml:"parent,attr,omitempty" json:"parent,omitempty"`
	Title       string    `xml:"title,attr" json:"title"`
	Album       string    `xml:"album,attr" json:"album"`
	AlbumID     tube.SSID `xml:"albumId,attr,omitempty" json:"albumId,omitempty"`
	Artist      string    `xml:"artist,attr" json:"artist"`
	ArtistID    tube.SSID `xml:"artistId,attr,omitempty" json:"artistId,omitempty"`
	Year        int       `xml:"year,attr,omitempty" json:"year,omitempty"`
	Genre       string    `xml:"genre,attr,omitempty" json:"genre,omitempty"`
	IsDir       bool      `xml:"isDir,attr" json:"isDir"`
	CoverArt    string    `xml:"coverArt,attr,omitempty" json:"coverArt,omitempty"`
	Created     string    `xml:"created,attr,omitempty" json:"created,omitempty"`
	Duration    int       `xml:"duration,attr,omitempty" json:"duration,omitempty"`
	Bitrate     int       `xml:"bitRate,attr,omitempty" json:"bitRate,omitempty"`
	Size        int       `xml:"size,attr,omitempty" json:"size,omitempty"`
	Suffix      string    `xml:"suffix,attr,omitempty" json:"suffix,omitempty"`
	ContentType string    `xml:"contentType,attr,omitempty" json:"contentType,omitempty"`
	IsVideo     bool      `xml:"isVideo,attr" json:"isVideo"`
	Path        string    `xml:"path,attr,omitempty" json:"path,omitempty"`
	PlayCount   int       `xml:"playCount,attr" json:"playCount"`
	Track       int       `xml:"track,attr,omitempty" json:"track,omitempty"`
	Disc        int       `xml:"discNumber,attr,omitempty" json:"discNumber,omitempty"`
	BookmarkPos int       `xml:"bookmarkPosition,attr,omitempty" json:"bookmarkPosition,omitempty"`
	Type        string    `xml:"type,attr" json:"type"`
	Starred     string    `xml:"starred,attr,omitempty" json:"starred,omitempty"`
}

func newSubsonicSong(t tube.Track, tagName string) subsonicSong {
	song := subsonicSong{
		XMLName:     xml.Name{Local: tagName},
		ID:          t.TrackSSID(),
		Parent:      t.AlbumSSID().String(),
		Title:       t.Info.Title,
		Album:       t.Info.Album,
		AlbumID:     t.AlbumSSID(),
		Artist:      t.Info.Artist,
		ArtistID:    t.ArtistSSID(),
		Year:        t.Year,
		Genre:       t.Genre,
		Created:     t.Date.Format(subsonicTimeLayout),
		Duration:    t.Duration,
		Size:        t.Size,
		Bitrate:     t.Bitrate(),
		Suffix:      strings.ToLower(t.Filetype),
		ContentType: t.MIMEType(),
		Path:        t.Filename,
		PlayCount:   t.Plays,
		Track:       t.Number,
		Disc:        t.Disc,
		BookmarkPos: sec2msec(t.Resume),
		IsDir:       false,
		IsVideo:     false,
		Type:        "music",
	}
	if t.Picture.ID != "" {
		song.CoverArt = t.TrackSSID().String()
	}
	if !t.Starred.IsZero() {
		song.Starred = t.Starred.Format(subsonicTimeLayout)
	}
	return song
}

func subsonicGetMusicDirectory(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	type subsonicMusicDir struct {
		XMLName  xml.Name      `xml:"directory" json:"-"`
		ID       string        `xml:"id,attr" json:"id"`
		Parent   string        `xml:"parent,attr" json:"parent,omitempty"`
		Name     string        `xml:"name,attr" json:"name"`
		Children []interface{} `xml:",omitempty" json:"child,omitempty"`
	}

	resp := struct {
		subsonicResponse
		Dir *subsonicMusicDir `json:"directory"`
	}{
		subsonicResponse: subOK(),
		Dir:              &subsonicMusicDir{},
	}

	rawid := r.FormValue("id")
	ssid := tube.ParseSSID(rawid)

	switch ssid.Kind {
	case tube.SSIDArtist:
		albums := lib.Albums(organize{
			ssid: ssid,
		})
		for _, a := range albums {
			first := a.tracks[0]
			resp.Dir.ID = first.ArtistSSID().String()
			resp.Dir.Name = first.Info.Artist

			dir := subsonicFolder{
				XMLName:  xml.Name{Local: "child"},
				ID:       first.AlbumSSID().String(),
				Parent:   first.ArtistSSID().String(),
				Title:    first.Info.Album,
				Artist:   first.Info.Artist,
				IsDir:    true,
				CoverArt: first.TrackSSID().String(),
			}
			resp.Dir.Children = append(resp.Dir.Children, dir)
		}
		writeSubsonic(ctx, w, r, resp)
		return
	case tube.SSIDAlbum:
		albums := lib.Albums(organize{
			ssid: ssid,
		})
		for _, a := range albums {
			first := a.tracks[0]
			resp.Dir.ID = first.AlbumSSID().String()
			resp.Dir.Parent = first.ArtistSSID().String()
			resp.Dir.Name = first.Info.Album

			for _, t := range a.tracks {
				song := newSubsonicSong(t, "child")
				if t.Picture.ID == first.Picture.ID {
					song.CoverArt = first.TrackSSID().String()
				}
				resp.Dir.Children = append(resp.Dir.Children, song)
			}
		}
		writeSubsonic(ctx, w, r, resp)
		return
	}

	panic("unknown ID?")
}

func subsonicGetSong(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	ssid := tube.ParseSSID(r.FormValue("id"))

	track, err := u.GetTrack(ctx, ssid.ID)
	if err == tube.ErrNotFound {
		writeSubsonic(ctx, w, r, subErr(70, "The requested data was not found."))
		return
	}
	if err != nil {
		panic(err)
	}

	type songResponse struct {
		subsonicResponse
		Song subsonicSong `json:"song"`
	}

	resp := songResponse{
		subsonicResponse: subOK(),
		Song:             newSubsonicSong(track, "song"),
	}

	writeSubsonic(ctx, w, r, resp)
}

func subsonicStream(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id := tube.ParseSSID(r.FormValue("id")).ID
	r.SetPathValue("id", id)
	downloadTrack(ctx, w, r)
}

func subsonicScrobble(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	id := tube.ParseSSID(r.FormValue("id")).ID

	if _, err := u.IncPlays(ctx, id); err != nil {
		if err == tube.ErrNotFound {
			writeSubsonic(ctx, w, r, subErr(70, "The requested data was not found."))
			return
		}
		panic(err)
	}

	writeSubsonic(ctx, w, r, subOK())
}

func subsonicGetCoverArt(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	rawid := r.FormValue("id")
	if rawid == "" {
		writeSubsonic(ctx, w, r, subErr(10, "Required parameter is missing."))
		return
	}
	id := tube.ParseSSID(r.FormValue("id"))

	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	var track tube.Track
	switch id.Kind {
	case tube.SSIDTrack:
		track, _ = lib.TrackByID(id.ID)
	case tube.SSIDAlbum:
		if a, ok := lib.albums[id.String()]; ok {
			if a.picture != "" {
				track, _ = lib.TrackByID(a.picture)
			}
		}
	case tube.SSIDArtist:
		if a, ok := lib.artists[id.String()]; ok {
			for _, t := range a.tracks {
				if t.Picture.ID != "" {
					track = t
					break
				}
			}
		}
	case tube.SSIDInvalid:
		if strings.HasPrefix(rawid, "pl-") {
			pid, err := strconv.Atoi(strings.TrimPrefix(rawid, "pl-"))
			if err != nil {
				panic(err)
			}
			pl, err := u.GetPlaylist(ctx, pid)
			if err != nil {
				panic(err)
			}
			for _, tid := range pl.Tracks {
				if t, ok := lib.TrackByID(tid); ok && t.Picture.ID != "" {
					track = t
					break
				}
			}
		}
		if !strings.ContainsRune(rawid, '-') {
			track, _ = lib.TrackByID(rawid)
		}
	}

	if track.Picture.ID == "" {
		writeSubsonic(ctx, w, r, subErr(70, "The requested data was not found."))
		return
	}

	servePicture(ctx, w, r, u, track.Picture.ID)
}

func subsonicGetRandomSongs(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	size, _ := strconv.Atoi(r.FormValue("size"))
	if size == 0 {
		size = 10
	}
	if size > 500 {
		size = 500
	}
	match := tube.ParseSSID(r.FormValue("musicFolderId"))

	tracks, err := u.GetTracks(ctx)
	if err != nil {
		panic(err)
	}

	result := make([]subsonicSong, 0, size)
	perm := rand.Perm(len(tracks))
	for _, idx := range perm {
		t := tracks[idx]
		if !match.IsZero() && !t.MatchesSSID(match) {
			continue
		}
		result = append(result, newSubsonicSong(t, "song"))
	}

	type randResponse struct {
		subsonicResponse
		Songs struct {
			List []subsonicSong `json:"song,omitempty"`
		} `xml:"randomSongs" json:"randomSongs"`
	}

	resp := randResponse{
		subsonicResponse: subOK(),
	}
	resp.Songs.List = result

	writeSubsonic(ctx, w, r, resp)
}

func subsonicGetLyrics(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp := struct {
		subsonicResponse
		Lyrics struct {
		} `xml:"lyrics" json:"lyrics"`
	}{
		subsonicResponse: subOK(),
	}
	writeSubsonic(ctx, w, r, resp)
}

func subsonicStar(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	r.ParseForm()
	now := time.Now().UTC()
	ids := append(r.Form["id"], append(r.Form["albumId"], r.Form["artistId"]...)...)
	for _, id := range ids {
		if err := u.SetStar(ctx, tube.ParseSSID(id), now); err != nil {
			panic(err)
		}
	}
	writeSubsonic(ctx, w, r, subOK())
}

func subsonicUnstar(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	r.ParseForm()
	ids := append(r.Form["id"], append(r.Form["albumId"], r.Form["artistId"]...)...)
	for _, id := range ids {
		if err := u.DeleteStar(ctx, id); err != nil {
			panic(err)
		}
	}
	writeSubsonic(ctx, w, r, subOK())
}
