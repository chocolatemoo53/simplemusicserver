package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/guregu/intertube/tube"
)

var (
	lrclibURL    = "https://lrclib.net/api/get"
	lrclibClient = &http.Client{Timeout: 10 * time.Second}
)

const (
	lrclibAgent   = "simplemusicserver (https://github.com/chocolatemoo53/simplemusicserver)"
	lyricsRecheck = 30 * 24 * time.Hour
)

func lookupLRCLIB(ctx context.Context, t tube.Track) (tube.Lyrics, error) {
	artist := t.Info.Artist
	if artist == "" {
		artist = t.Info.AlbumArtist
	}
	if artist == "" || t.Info.Title == "" || t.Duration == 0 {
		return tube.Lyrics{}, nil
	}
	q := url.Values{
		"artist_name": {artist},
		"track_name":  {t.Info.Title},
		"album_name":  {t.Info.Album},
		"duration":    {strconv.Itoa(t.Duration)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lrclibURL+"?"+q.Encode(), nil)
	if err != nil {
		return tube.Lyrics{}, err
	}
	req.Header.Set("User-Agent", lrclibAgent)
	resp, err := lrclibClient.Do(req)
	if err != nil {
		return tube.Lyrics{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return tube.Lyrics{}, nil
	default:
		return tube.Lyrics{}, fmt.Errorf("lrclib: status %d", resp.StatusCode)
	}
	var found struct {
		Plain        string `json:"plainLyrics"`
		Synced       string `json:"syncedLyrics"`
		Instrumental bool   `json:"instrumental"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&found); err != nil {
		return tube.Lyrics{}, fmt.Errorf("lrclib: %w", err)
	}
	lyrics := tube.ParseLyrics(found.Synced)
	if lyrics.Synced == "" {
		lyrics = tube.ParseLyrics(found.Plain)
	} else if found.Plain != "" {
		lyrics.Plain = tube.ParseLyrics(found.Plain).Plain
	}
	lyrics.Instrumental = found.Instrumental
	lyrics.Source = tube.LyricsFromLRCLIB
	return lyrics, nil
}

func trackLyrics(ctx context.Context, u tube.User, id string) (tube.Track, *tube.Lyrics, error) {
	track, err := u.GetTrack(ctx, id)
	if err != nil {
		return track, nil, err
	}
	if l := track.Lyrics; l != nil && (!l.Empty() || l.Instrumental || l.Source == tube.LyricsFromFile || time.Since(l.Checked) < lyricsRecheck) {
		return track, l, nil
	}
	if !u.Display.LyricsLookup {
		return track, track.Lyrics, nil
	}
	found, err := lookupLRCLIB(ctx, track)
	if err != nil {
		return track, track.Lyrics, err
	}
	found.Checked = time.Now().UTC()
	if found.Empty() && !found.Instrumental {
		found.Source = ""
	}
	updated, err := u.UpdateTrack(ctx, id, func(t *tube.Track) error {
		t.Lyrics = &found
		return nil
	})
	if err != nil {
		return track, track.Lyrics, err
	}
	return updated, updated.Lyrics, nil
}

type lyricsResponse struct {
	Synced       bool
	Instrumental bool
	Source       string
	Lookup       bool
	Failed       bool
	Lines        []tube.LyricLine
}

func showLyrics(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	_, lyrics, err := trackLyrics(ctx, u, r.PathValue("id"))
	if errors.Is(err, tube.ErrNotFound) {
		renderText(w, "track not found", http.StatusNotFound)
		return
	}
	resp := lyricsResponse{Lookup: u.Display.LyricsLookup}
	if err != nil {
		log.Println("lyrics lookup failed for user", u.ID, err)
		resp.Failed = true
	}
	if lyrics != nil {
		resp.Synced = lyrics.Synced != ""
		resp.Instrumental = lyrics.Instrumental
		resp.Source = lyrics.Source
		if !lyrics.Empty() {
			resp.Lines = lyrics.Lines()
		}
	}
	renderJSON(w, resp, http.StatusOK)
}

type subsonicLyricLine struct {
	Start *int64 `xml:"start,attr,omitempty" json:"start,omitempty"`
	Value string `xml:",chardata" json:"value"`
}

type subsonicStructuredLyrics struct {
	DisplayArtist string              `xml:"displayArtist,attr" json:"displayArtist"`
	DisplayTitle  string              `xml:"displayTitle,attr" json:"displayTitle"`
	Lang          string              `xml:"lang,attr" json:"lang"`
	Offset        int                 `xml:"offset,attr" json:"offset"`
	Synced        bool                `xml:"synced,attr" json:"synced"`
	Lines         []subsonicLyricLine `xml:"line" json:"line"`
}

type subsonicLyricsList struct {
	subsonicResponse
	LyricsList struct {
		Structured []subsonicStructuredLyrics `xml:"structuredLyrics" json:"structuredLyrics,omitempty"`
	} `xml:"lyricsList" json:"lyricsList"`
}

func subsonicGetLyricsBySongID(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	track, lyrics, err := trackLyrics(ctx, u, tube.ParseSSID(r.FormValue("id")).ID)
	if errors.Is(err, tube.ErrNotFound) {
		writeSubsonic(ctx, w, r, subErr(70, "The requested data was not found."))
		return
	}
	if err != nil {
		log.Println("lyrics lookup failed for user", u.ID, err)
	}
	resp := subsonicLyricsList{subsonicResponse: subOK()}
	if lyrics != nil && !lyrics.Empty() {
		entry := subsonicStructuredLyrics{
			DisplayArtist: track.Info.Artist,
			DisplayTitle:  track.Info.Title,
			Lang:          "und",
			Synced:        lyrics.Synced != "",
		}
		for _, line := range lyrics.Lines() {
			sl := subsonicLyricLine{Value: line.Text}
			if entry.Synced {
				start := line.Start
				sl.Start = &start
			}
			entry.Lines = append(entry.Lines, sl)
		}
		resp.LyricsList.Structured = append(resp.LyricsList.Structured, entry)
	}
	writeSubsonic(ctx, w, r, resp)
}
