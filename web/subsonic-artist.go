package web

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/guregu/intertube/tube"
)

type subsonicArtist struct {
	XMLName    xml.Name        `xml:"artist" json:"-"`
	ID         tube.SSID       `xml:"id,attr" json:"id"`
	Name       string          `xml:"name,attr" json:"name"`
	CoverArt   string          `xml:"coverArt,attr,omitempty" json:"coverArt,omitempty"`
	AlbumCount int             `xml:"albumCount,attr" json:"albumCount"`
	Starred    string          `xml:"starred,attr,omitempty" json:"starred,omitempty"`
	Albums     []subsonicAlbum `xml:",omitempty" json:"album,omitempty"`
}

func newSubsonicArtist(a *artistInfo) subsonicArtist {
	artist := subsonicArtist{
		ID:         a.ssid,
		Name:       a.name,
		AlbumCount: len(a.albums),
	}
	for _, t := range a.tracks {
		if t.Picture.ID != "" {
			artist.CoverArt = t.TrackSSID().String()
			break
		}
	}
	return artist
}

func subsonicGetArtists(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	type artistsResp struct {
		subsonicResponse
		Indexes struct {
			IgnoredArticles string          `xml:"ignoredArticles,attr" json:"ignoredArticles"`
			List            []subsonicIndex `json:"index,omitempty"`
		} `xml:"artists" json:"artists"`
	}

	resp := artistsResp{
		subsonicResponse: subOK(),
	}
	resp.Indexes.IgnoredArticles = subsonicIgnoreArticles
	resp.Indexes.List = newSubsonicIndexes(lib.Artists(organize{}))

	writeSubsonic(ctx, w, r, resp)
}

func subsonicGetIndexes(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	type artistsResp struct {
		subsonicResponse
		Indexes struct {
			IgnoredArticles string          `xml:"ignoredArticles,attr" json:"ignoredArticles"`
			List            []subsonicIndex `json:"index,omitempty"`
		} `xml:"indexes" json:"indexes"`
	}

	resp := artistsResp{
		subsonicResponse: subOK(),
	}
	resp.Indexes.IgnoredArticles = subsonicIgnoreArticles
	resp.Indexes.List = newSubsonicIndexes(lib.Artists(organize{}))

	writeSubsonic(ctx, w, r, resp)
}

func subsonicGetArtist(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	rawid := r.FormValue("id")
	ssid := tube.ParseSSID(rawid)

	lib, err := getLibrary(ctx, u)
	if err != nil {
		panic(err)
	}

	artist, ok := lib.artists[ssid.String()]
	if !ok {
		writeSubsonic(ctx, w, r, subErr(70, "The requested data was not found."))
		return
	}

	allAlbums := lib.Albums(organize{ssid: artist.ssid})
	var albums []subsonicAlbum
	for _, a := range allAlbums {
		albums = append(albums, newSubsonicAlbum(a.tracks, false))
	}

	artistResp := newSubsonicArtist(artist)
	artistResp.Albums = albums

	resp := struct {
		subsonicResponse
		Artist subsonicArtist `xml:"artist" json:"artist"`
	}{
		subsonicResponse: subOK(),
		Artist:           artistResp,
	}

	writeSubsonic(ctx, w, r, resp)
}

func subsonicGetArtistInfo(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	type info struct {
		Biography      string `xml:"biography,omitempty" json:"biography,omitempty"`
		MusicBrainzId  string `xml:"musicBrainzId,omitempty" json:"musicBrainzId,omitempty"`
		LastFmUrl      string `xml:"lastFmUrl,omitempty" json:"lastFmUrl,omitempty"`
		SmallImageUrl  string `xml:"smallImageUrl,omitempty" json:"smallImageUrl,omitempty"`
		MediumImageUrl string `xml:"mediumImageUrl,omitempty" json:"mediumImageUrl,omitempty"`
		LargeImageUrl  string `xml:"largeImageUrl,omitempty" json:"largeImageUrl,omitempty"`
	}
	resp := struct {
		subsonicResponse
		Info  *info `xml:"artistInfo,omitempty" json:"artistInfo,omitempty"`
		Info2 *info `xml:"artistInfo2,omitempty" json:"artistInfo2,omitempty"`
	}{
		subsonicResponse: subOK(),
	}

	ai := &info{}
	if strings.Contains(r.URL.Path, "Info2") {
		resp.Info2 = ai
	} else {
		resp.Info = ai
	}

	writeSubsonic(ctx, w, r, resp)
}
