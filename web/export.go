package web

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/guregu/intertube/tube"
)

type exportTrack struct {
	Path        string
	Title       string
	Artist      string
	Album       string
	AlbumArtist string `json:",omitempty"`
	Genre       string `json:",omitempty"`
	Year        int    `json:",omitempty"`
	Disc        int    `json:",omitempty"`
	Number      int    `json:",omitempty"`
	Duration    int    `json:",omitempty"`
	Plays       int
	LastPlayed  time.Time `json:",omitzero"`
	Added       time.Time
	Starred     bool `json:",omitempty"`
}

type exportPlaylist struct {
	Name    string
	Desc    string `json:",omitempty"`
	Dynamic bool   `json:",omitempty"`
	Query   string `json:",omitempty"`
	Tracks  []string
}

func uniquePath(used map[string]bool, p string) string {
	ext := path.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for n := 2; used[strings.ToLower(p)]; n++ {
		p = fmt.Sprintf("%s (%d)%s", base, n, ext)
	}
	used[strings.ToLower(p)] = true
	return p
}

func exportLibrary(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)
	tracks, err := u.GetTracks(ctx)
	if err != nil {
		panic(err)
	}
	playlists, err := u.GetPlaylists(ctx)
	if err != nil {
		panic(err)
	}
	stars, err := u.GetStars(ctx)
	if err != nil {
		panic(err)
	}

	used := map[string]bool{"library.json": true}
	paths := make(map[string]string, len(tracks))
	meta := make([]exportTrack, 0, len(tracks))
	for _, t := range tracks {
		p := uniquePath(used, t.VirtualPath())
		paths[t.ID] = p
		_, starred := stars[t.TrackSSID()]
		meta = append(meta, exportTrack{
			Path: p, Title: t.Info.Title, Artist: t.Info.Artist, Album: t.Info.Album, AlbumArtist: t.Info.AlbumArtist,
			Genre: t.Genre, Year: t.Year, Disc: t.Disc, Number: t.Number, Duration: t.Duration,
			Plays: t.Plays, LastPlayed: t.LastPlayed, Added: t.Date, Starred: starred,
		})
	}

	name := "music-library-" + time.Now().UTC().Format(time.DateOnly) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)

	for _, t := range tracks {
		if err := exportFile(ctx, zw, u, t, paths[t.ID]); err != nil {
			log.Println("export: user", u.ID, err)
			return
		}
	}

	var lists []exportPlaylist
	for _, pl := range playlists {
		entry := exportPlaylist{Name: pl.Name, Desc: pl.Desc, Dynamic: pl.Dynamic, Query: pl.Query}
		var m3u strings.Builder
		m3u.WriteString("#EXTM3U\n")
		for _, id := range pl.Tracks {
			if p, ok := paths[id]; ok {
				entry.Tracks = append(entry.Tracks, p)
				m3u.WriteString("../" + p + "\n")
			}
		}
		lists = append(lists, entry)
		if pl.Dynamic {
			continue
		}
		listName := strings.Trim(strings.NewReplacer("/", "-", "\\", "-", "..", "").Replace(pl.Name), " .")
		if listName == "" {
			listName = "Playlist"
		}
		f, err := zw.Create(uniquePath(used, "Playlists/"+listName+".m3u8"))
		if err != nil {
			log.Println("export: user", u.ID, err)
			return
		}
		if _, err := io.WriteString(f, m3u.String()); err != nil {
			log.Println("export: user", u.ID, err)
			return
		}
	}

	f, err := zw.Create("library.json")
	if err == nil {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		err = enc.Encode(struct {
			Exported  time.Time
			Tracks    []exportTrack
			Playlists []exportPlaylist
		}{time.Now().UTC(), meta, lists})
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		log.Println("export: user", u.ID, err)
	}
}

func exportFile(ctx context.Context, zw *zip.Writer, u tube.User, t tube.Track, name string) error {
	fileKey, err := u.TrackFileKey(t)
	if err != nil {
		return err
	}
	reader, err := tube.NewDecryptingReader(int64(t.Size), fileKey, t.Object, objectFetcher(ctx, t.Object))
	if err != nil {
		return err
	}
	f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: t.LastModOrDate()})
	if err != nil {
		return err
	}
	_, err = io.Copy(f, reader)
	return err
}
