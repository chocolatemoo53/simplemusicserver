package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/guregu/tag"
	"github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

func uploadForm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(ctx)

	tracks, err := u.GetTracks(ctx)
	if err != nil {
		panic(err)
	}
	lib := NewLibrary(tracks, nil)
	type meta struct {
		LastMod int64
		Size    int
	}
	dupes := make(map[string][]meta)
	for _, t := range lib.Tracks(organize{}) {
		dupes[t.Filename] = append(dupes[t.Filename], meta{
			Size:    t.Size,
			LastMod: t.LocalMod,
		})
	}

	data := struct {
		User  tube.User
		Dupes map[string][]meta
	}{
		User:  u,
		Dupes: dupes,
	}
	renderTemplate(ctx, w, "upload", data, http.StatusOK)
}

func processUpload(ctx context.Context, user tube.User, fmeta tube.File, data []byte) (tube.Track, error) {
	raw := bytes.NewReader(data)

	_, format, err := tag.Identify(raw)
	if err != tag.ErrNoTagsFound && err != nil {
		return tube.Track{}, err
	}
	if format == tag.UnknownFileType {
		switch strings.ToLower(path.Ext(fmeta.Name)) {
		case ".mp3":
			format = tag.MP3
		case ".flac":
			format = tag.FLAC
		case ".m4a":
			format = tag.M4A
		}
	}
	if format != tag.MP3 && format != tag.FLAC && format != tag.M4A {
		return tube.Track{}, fmt.Errorf("only mp3/flac/m4a supported right now (got: %v)", format)
	}
	raw.Seek(0, io.SeekStart)

	dur, err := calcDuration(raw, format)
	if err != nil && !skippableError(err) {
		return tube.Track{}, err
	}
	raw.Seek(0, io.SeekStart)

	var tags multiMeta
	if got, err := tag.ReadID3v2Tags(raw); err == nil {
		tags = append(tags, got)
	}
	raw.Seek(0, io.SeekStart)
	if got, err := tag.ReadFrom(raw); err == nil {
		tags = append(tags, got)
	}
	raw.Seek(0, io.SeekStart)
	tags = append(tags, guessMetadata(fmeta.Name, format))
	unfuckID3(tags)
	raw.Seek(0, io.SeekStart)

	sum, err := tag.SumAll(raw)
	if err != nil {
		return tube.Track{}, err
	}

	trackInfo := tube.TrackInfo{
		Title:       tags.Title(),
		Artist:      tags.Artist(),
		Album:       tags.Album(),
		AlbumArtist: tags.AlbumArtist(),
		Composer:    tags.Composer(),
		Genre:       tags.Genre(),
		Comment:     tags.Comment(),
	}
	trackInfo.Sanitize()

	trackID := user.TrackID(sum)
	if existing, err := user.GetTrack(ctx, trackID); err == nil {
		return existing, nil
	} else if err != tube.ErrNotFound {
		return tube.Track{}, err
	}

	track := tube.Track{
		UserID:      user.ID,
		ID:          trackID,
		ContentHash: sum,
		Object:      tube.NewObjectKey(user.ID),

		Year: tags.Year(),

		Filename: strings.ToValidUTF8(fmeta.Name, replacementChar),
		Filetype: string(tags.FileType()),
		UploadID: fmeta.ID,
		Size:     len(data),
		LocalMod: fmeta.LocalMod,
		Duration: dur,

		TagFormat: string(tags.Format()),
	}
	track.Number, track.Total = tags.Track()
	track.Disc, track.Discs = tags.Disc()
	track.ApplyInfo(trackInfo)
	if text := tags.Lyrics(); strings.TrimSpace(text) != "" {
		lyrics := tube.ParseLyrics(text)
		lyrics.Source = tube.LyricsFromFile
		track.Lyrics = &lyrics
	}

	fileKey, wrapped, err := user.NewTrackFileKey(track.Object)
	if err != nil {
		return tube.Track{}, err
	}
	track.FileKey = wrapped
	if err := encryptToBucket(ctx, track.Object, fileKey, data); err != nil {
		return tube.Track{}, err
	}

	if pic := tags.Picture(); pic != nil {
		track.Picture, err = savePicture(ctx, user, pic.Data, pic.Ext, pic.Type, pic.Description)
		if err != nil {
			storage.Delete(ctx, track.Object)
			return tube.Track{}, err
		}
	}

	if err := user.CreateTrack(ctx, &track); err != nil {
		storage.Delete(ctx, track.Object)
		return tube.Track{}, err
	}
	return track, nil
}

var replacementChar = "�"

func savePicture(ctx context.Context, u tube.User, data []byte, ext, mimetype, desc string) (tube.Picture, error) {
	pic := u.PictureFor(data, ext, mimetype, desc)
	if _, err := tube.ObjectSize(ctx, u.ID, pic.Object); err == nil {
		return pic, nil
	}
	enc := tube.EncryptBytes(data, u.PictureKey(pic.ID), pic.Object)
	if err := storage.FilesBucket.Put(ctx, "application/octet-stream", pic.Object, bytes.NewReader(enc)); err != nil {
		return tube.Picture{}, err
	}
	return pic, tube.RecordObject(ctx, u.ID, pic.Object, tube.ObjectPicture, pic.Size)
}

func calcDuration(r io.ReadSeeker, ftype tag.FileType) (int, error) {
	switch ftype {
	case tag.MP3:
		dec, err := mp3.NewDecoder(r)
		if err != nil {
			if strings.Contains(err.Error(), "free bitrate") {
				return 0, nil
			}
			return 0, err
		}
		sr := dec.SampleRate()
		length := dec.Length()
		if sr == 0 {
			return 0, nil
		}
		return (int(length) / sr) / 4, nil
	case tag.FLAC:
		stream, err := flac.Parse(r)
		if err != nil {
			return 0, err
		}
		defer stream.Close()
		sec := stream.Info.NSamples / uint64(stream.Info.SampleRate)
		return int(sec), nil
	case tag.M4A:
		return 0, nil
	}
	return 0, fmt.Errorf("unknown type: %v", ftype)
}

func skippableError(err error) bool {
	if err == nil {
		return true
	}
	str := err.Error()
	return strings.Contains(str, "mp3:")
}
