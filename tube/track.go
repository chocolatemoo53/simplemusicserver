package tube

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	UnknownArtist = "Unknown Artist"
	UnknownAlbum  = "Unknown Album"
	UnknownTitle  = "Untitled"
)

type Track struct {
	UserID int
	ID     string
	SortID string
	Date   time.Time

	Info TrackInfo

	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Composer    string
	Genre       string
	Comment     string

	Number  int
	Total   int
	Disc    int
	Discs   int
	Year    int
	Picture Picture
	Tags    []string

	Filename string
	Filetype string
	UploadID string
	Size     int
	Duration int

	TagFormat string
	Lyrics    *Lyrics `json:",omitempty"`

	LastMod  time.Time
	LocalMod int64
	Dirty    bool

	Plays      int
	LastPlayed time.Time
	Resume     float64
	ResumeMod  time.Time

	ContentHash string
	FileKey     []byte

	Object  string    `json:"-"`
	Starred time.Time `json:"-"`
	DL      string    `json:",omitempty"`
}

type TrackInfo struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Composer    string
	Genre       string
	Comment     string
}

func (ti *TrackInfo) Sanitize() {
	const replacement = "\ufffd"
	ti.Title = strings.ToValidUTF8(ti.Title, replacement)
	ti.Artist = strings.ToValidUTF8(ti.Artist, replacement)
	ti.Album = strings.ToValidUTF8(ti.Album, replacement)
	ti.AlbumArtist = strings.ToValidUTF8(ti.AlbumArtist, replacement)
	ti.Composer = strings.ToValidUTF8(ti.Composer, replacement)
	ti.Genre = strings.ToValidUTF8(ti.Genre, replacement)
	ti.Comment = strings.ToValidUTF8(ti.Comment, replacement)
}

func (t TrackInfo) AnyArtist() string {
	if t.AlbumArtist != "" {
		return t.AlbumArtist
	}
	if t.Artist != "" {
		return t.Artist
	}
	if t.Composer != "" {
		return t.Composer
	}
	return ""
}

func (t *Track) ApplyInfo(info TrackInfo) {
	t.Info = info
	t.Title = strings.ToLower(info.Title)
	t.Artist = strings.ToLower(info.Artist)
	t.Album = strings.ToLower(info.Album)
	t.AlbumArtist = strings.ToLower(info.AlbumArtist)
	t.Composer = strings.ToLower(info.Composer)
	t.Genre = strings.ToLower(info.Genre)
	t.Comment = strings.ToLower(info.Comment)
}

var ErrLocked = errors.New("library is locked")

func trackAD(userID int, id string) string {
	return fmt.Sprintf("tracks:%d:%s", userID, id)
}

func (u User) TrackID(contentHash string) string {
	return u.keys.ID("track", contentHash)
}

func NewObjectKey(userID int) string {
	return fmt.Sprintf("u/%d/%s", userID, hex.EncodeToString(randomBytes(16)))
}

func (u User) sealTrack(t Track) ([]byte, error) {
	if u.keys == nil {
		return nil, ErrLocked
	}
	t.Starred = time.Time{}
	t.DL = ""
	return u.keys.SealJSON(t, trackAD(u.ID, t.ID))
}

func (u User) openTrack(id, object string, data []byte) (Track, error) {
	if u.keys == nil {
		return Track{}, ErrLocked
	}
	var t Track
	if err := u.keys.OpenJSON(data, trackAD(u.ID, id), &t); err != nil {
		return Track{}, err
	}
	t.UserID, t.ID, t.Object = u.ID, id, object
	t.SortID = t.SortKey()
	return t, nil
}

func (u User) saveTrack(ctx context.Context, q querier, t Track) error {
	data, err := u.sealTrack(t)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO tracks (user_id, id, object, size, data) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, id) DO UPDATE SET object = excluded.object, size = excluded.size, data = excluded.data`,
		u.ID, t.ID, t.Object, t.Size, data)
	return err
}

func (u User) getTrack(ctx context.Context, q querier, id string) (Track, error) {
	var (
		object string
		data   []byte
	)
	err := q.QueryRowContext(ctx, `SELECT object, data FROM tracks WHERE user_id = ? AND id = ?`, u.ID, id).Scan(&object, &data)
	if err != nil {
		return Track{}, notFound(err)
	}
	return u.openTrack(id, object, data)
}

func (u User) queryTracks(ctx context.Context, query string, args ...any) (Tracks, error) {
	if u.keys == nil {
		return nil, ErrLocked
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks Tracks
	for rows.Next() {
		var (
			id, object string
			data       []byte
		)
		if err := rows.Scan(&id, &object, &data); err != nil {
			return nil, err
		}
		t, err := u.openTrack(id, object, data)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (u User) CreateTrack(ctx context.Context, t *Track) error {
	t.UserID = u.ID
	t.Date = time.Now().UTC()
	t.SortID = t.SortKey()
	return inTx(ctx, func(tx *sql.Tx) error {
		if _, err := u.getTrack(ctx, tx, t.ID); err == nil {
			return fmt.Errorf("track already exists: %s", t.ID)
		} else if err != ErrNotFound {
			return err
		}
		if err := u.saveTrack(ctx, tx, *t); err != nil {
			return err
		}
		if err := recordObject(ctx, tx, u.ID, t.Object, ObjectTrack, int64(t.Size)); err != nil {
			return err
		}
		_, err := updateUserTx(ctx, tx, u.ID, addUsage(int64(t.Size), 1))
		return err
	})
}

func (u User) SaveTrack(ctx context.Context, t *Track) error {
	t.SortID = t.SortKey()
	return u.saveTrack(ctx, db, *t)
}

func (u User) UpdateTrack(ctx context.Context, id string, fn func(*Track) error) (Track, error) {
	var updated Track
	err := inTx(ctx, func(tx *sql.Tx) error {
		t, err := u.getTrack(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := fn(&t); err != nil {
			return err
		}
		t.SortID = t.SortKey()
		updated = t
		return u.saveTrack(ctx, tx, t)
	})
	return updated, err
}

func (u User) IncPlays(ctx context.Context, id string) (Track, error) {
	return u.UpdateTrack(ctx, id, func(t *Track) error {
		t.Plays++
		t.LastPlayed = time.Now().UTC()
		return nil
	})
}

func (u User) SetResume(ctx context.Context, id string, secs float64, modTime time.Time) (Track, error) {
	return u.UpdateTrack(ctx, id, func(t *Track) error {
		if t.ResumeMod.After(modTime) {
			return ErrConflict
		}
		t.Resume = secs
		t.ResumeMod = modTime
		return nil
	})
}

func (u User) SetDuration(ctx context.Context, id string, secs int) (Track, error) {
	return u.UpdateTrack(ctx, id, func(t *Track) error {
		if t.Duration > secs {
			return ErrConflict
		}
		t.Duration = secs
		return nil
	})
}

type StoredTrack struct {
	ID     string
	Object string
	Size   int64
}

func TrackObjects(ctx context.Context, userID int) ([]StoredTrack, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, object, size FROM tracks WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []StoredTrack
	for rows.Next() {
		var st StoredTrack
		if err := rows.Scan(&st.ID, &st.Object, &st.Size); err != nil {
			return nil, err
		}
		list = append(list, st)
	}
	return list, rows.Err()
}

func RemoveTrack(ctx context.Context, userID int, id string) (string, error) {
	var object string
	err := inTx(ctx, func(tx *sql.Tx) error {
		var size int64
		err := tx.QueryRowContext(ctx, `SELECT object, size FROM tracks WHERE user_id = ? AND id = ?`, userID, id).Scan(&object, &size)
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tracks WHERE user_id = ? AND id = ?`, userID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE key = ?`, object); err != nil {
			return err
		}
		_, err = updateUserTx(ctx, tx, userID, addUsage(-size, -1))
		return err
	})
	return object, err
}

func (t Track) StorageKey() string {
	return t.Object
}

func (t Track) SortKey() string {
	artist := strings.ReplaceAll(t.AnyArtist(), " ", "-")
	if artist == "" {
		artist = "~{artist}"
	}
	if len(artist) > 100 {
		artist = artist[:100]
	}
	album := strings.ReplaceAll(t.Album, " ", "-")
	if album == "" {
		album = "~{album}"
	}
	if len(album) > 100 {
		album = album[:100]
	}
	title := strings.ReplaceAll(t.Title, " ", "-")
	if title == "" {
		title = strings.ReplaceAll(t.Filename, " ", "-")
	}
	if title == "" {
		title = "~{title}"
	}
	if len(title) > 100 {
		title = title[:100]
	}
	return fmt.Sprintf("%s %s %04d %06d %s %s", artist, album, t.Disc, t.Number, title, t.ID)
}

func (t Track) FileURL() string {
	return fmt.Sprintf("/dl/tracks/%s%s", t.ID, path.Ext(t.Filename))
}

func (t Track) VirtualPath() string {
	scrub := func(name string) string {
		name = strings.ReplaceAll(name, "/", "-")
		name = strings.ReplaceAll(name, "\\", "-")
		name = strings.ReplaceAll(name, "...", "")
		name = strings.ReplaceAll(name, "..", "")
		const cutset = `<>:"|?*`
		for _, chr := range cutset {
			name = strings.ReplaceAll(name, string(chr), "")
		}
		return strings.Trim(name, " .")
	}
	artist := t.Info.AlbumArtist
	if artist == "" {
		if t.Info.Artist != "" {
			artist = t.Info.Artist
		} else {
			artist = "Unknown Artist"
		}
	}
	if artist = scrub(artist); artist == "" {
		artist = "Unknown Artist"
	}
	album := scrub(t.Info.Album)
	if album == "" {
		album = "Unknown Album"
	}
	title := t.Info.Title
	if title == "" {
		if t.Filename != "" {
			title = t.Filename
		} else {
			title = "Untitled"
		}
	}
	var num string
	if t.Disc != 0 && t.Number != 0 {
		num = fmt.Sprintf("%d-%02d ", t.Disc, t.Number)
	} else if t.Number != 0 {
		num = fmt.Sprintf("%02d ", t.Number)
	}
	filename := scrub(num + title + t.Ext())
	if filename == "" || filename == t.Ext() {
		filename = "Untitled" + t.Ext()
	}
	return path.Join(artist, album, filename)
}

func (t Track) LastModOrDate() time.Time {
	if !t.LastMod.IsZero() {
		return t.LastMod
	}
	return t.Date
}

func (t Track) MIMEType() string {
	switch t.Filetype {
	case "MP3":
		return "audio/mp3"
	case "FLAC":
		return "audio/flac"
	case "M4A":
		return "audio/mp4"
	}
	return ""
}

func (t Track) Ext() string {
	switch t.Filetype {
	case "MP3":
		return ".mp3"
	case "FLAC":
		return ".flac"
	case "M4A":
		return ".m4a"
	}
	return ""
}

func (t Track) ArtistEqual(other Track) bool {
	if t.Artist != "" && t.Artist == other.Artist {
		return true
	}
	if t.AlbumArtist != "" && t.AlbumArtist == other.AlbumArtist {
		return true
	}
	if t.Composer != "" && t.Composer == other.Composer {
		return true
	}
	return false
}

func (t Track) AlbumEqual(other Track) bool {
	if !t.ArtistEqual(other) {
		return false
	}
	return t.Album == other.Album
}

func (t Track) AnyArtist() string {
	if t.AlbumArtist != "" {
		return t.AlbumArtist
	}
	if t.Artist != "" {
		return t.Artist
	}
	if t.Composer != "" {
		return t.Composer
	}
	return ""
}

func (t Track) AlbumCode() string {
	artist := t.AnyArtist()
	b := make([]byte, 0, len(artist)+1+len(t.Album))
	b = append(b, []byte(artist)...)
	b = append(b, '\x1F')
	b = append(b, []byte(t.Album)...)
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (t Track) ArtistSSID() SSID {
	return NewSSID(SSIDArtist, t.AnyArtist())
}

func (t Track) AlbumSSID() SSID {
	return NewSSID(SSIDAlbum, t.AlbumCode())
}

func (t Track) TrackSSID() SSID {
	return NewSSID(SSIDTrack, t.ID)
}

func (t Track) MatchesSSID(ssid SSID) bool {
	switch ssid.Kind {
	default:
		fallthrough
	case SSIDTrack, SSIDInvalid:
		return ssid.ID == t.ID
	case SSIDAlbum:
		return t.AlbumSSID() == ssid
	case SSIDArtist:
		return t.ArtistSSID() == ssid
	case SSIDFolder:
		return ssid == MusicFolder
	}
}

func (t Track) Bitrate() int {
	if t.Duration == 0 {
		return 320
	}
	guess := float64(t.Size) * 0.008 / float64(t.Duration)
	return int(guess)
}

type Picture struct {
	ID     string
	Type   string
	Ext    string
	Desc   string
	Object string
	Size   int64
}

func (p Picture) StorageKey() string {
	return p.Object
}

func (p Picture) URL() string {
	if p.ID == "" {
		return ""
	}
	return "/pic/" + p.ID
}

func (u User) GetTracks(ctx context.Context) (Tracks, error) {
	tracks, err := u.queryTracks(ctx, `SELECT id, object, data FROM tracks WHERE user_id = ?`, u.ID)
	if err != nil {
		return nil, err
	}
	sort.Sort(tracks)
	return tracks, nil
}

func (u User) GetTracksPartial(ctx context.Context, limit int, after string) (tracks Tracks, next string, err error) {
	tracks, err = u.queryTracks(ctx,
		`SELECT id, object, data FROM tracks WHERE user_id = ? AND id > ? ORDER BY id LIMIT ?`,
		u.ID, after, limit)
	if err != nil {
		return nil, "", err
	}
	if len(tracks) == limit {
		next = tracks[len(tracks)-1].ID
	}
	return tracks, next, nil
}

func (u User) GetTracksBatch(ctx context.Context, trackIDs []string) (Tracks, error) {
	if len(trackIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(trackIDs)+1)
	args = append(args, u.ID)
	for _, id := range trackIDs {
		args = append(args, id)
	}
	return u.queryTracks(ctx,
		`SELECT id, object, data FROM tracks WHERE user_id = ? AND id IN (`+placeholders(len(trackIDs))+`)`,
		args...)
}

func (u User) GetTrack(ctx context.Context, trackID string) (Track, error) {
	return u.getTrack(ctx, db, trackID)
}

func (u User) MassUpdateTracks(ctx context.Context, trackIDs []string, mutate func(*Track)) error {
	return inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range trackIDs {
			t, err := u.getTrack(ctx, tx, id)
			if err == ErrNotFound {
				continue
			}
			if err != nil {
				return err
			}
			mutate(&t)
			t.SortID = t.SortKey()
			if err := u.saveTrack(ctx, tx, t); err != nil {
				return err
			}
		}
		return nil
	})
}

func IncTotalPlays(ctx context.Context, secs int) error {
	if err := incCounter(ctx, "TotalPlays", 1); err != nil {
		return err
	}
	if secs <= 0 {
		return nil
	}
	return incCounter(ctx, "TotalTime", secs)
}

type Tracks []Track

func (tt Tracks) IDs() []string {
	ids := make([]string, 0, len(tt))
	for _, t := range tt {
		ids = append(ids, t.ID)
	}
	return ids
}

func (tt Tracks) Less(i, j int) bool {
	return tt[i].SortID < tt[j].SortID
}

func (tt Tracks) Swap(i, j int) {
	tt[i], tt[j] = tt[j], tt[i]
}

func (tt Tracks) Len() int {
	return len(tt)
}

var _ sort.Interface = Tracks{}
