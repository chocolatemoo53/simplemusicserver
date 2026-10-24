package tube

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Playlist struct {
	UserID int
	ID     int

	Date time.Time
	Name string
	Desc string

	Tracks   []string
	Duration int

	Dynamic bool
	Query   string
	Sort    []string
	UIMeta  []byte

	LastMod time.Time
}

func playlistAD(userID, id int) string {
	return fmt.Sprintf("playlists:%d:%d", userID, id)
}

func (u User) CreatePlaylist(ctx context.Context, p *Playlist) error {
	if u.keys == nil {
		return ErrLocked
	}
	if p.ID != 0 {
		return fmt.Errorf("already exists: %d", p.ID)
	}
	p.UserID = u.ID
	p.Date = time.Now().UTC()
	p.LastMod = p.Date
	return inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO playlists (user_id, data) VALUES (?, x'')`, u.ID)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		p.ID = int(id)
		data, err := u.keys.SealJSON(p, playlistAD(u.ID, p.ID))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE playlists SET data = ? WHERE id = ?`, data, p.ID)
		return err
	})
}

func (u User) SavePlaylist(ctx context.Context, p *Playlist) error {
	if u.keys == nil {
		return ErrLocked
	}
	p.UserID = u.ID
	p.LastMod = time.Now().UTC()
	data, err := u.keys.SealJSON(p, playlistAD(u.ID, p.ID))
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE playlists SET data = ? WHERE user_id = ? AND id = ?`, data, u.ID, p.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Playlist) With(tracks []Track) {
	p.Tracks = make([]string, 0, len(tracks))
	p.Duration = 0
	for _, t := range tracks {
		p.Duration += t.Duration
		p.Tracks = append(p.Tracks, t.ID)
	}
}

func (u User) openPlaylist(id int, data []byte) (Playlist, error) {
	var p Playlist
	if err := u.keys.OpenJSON(data, playlistAD(u.ID, id), &p); err != nil {
		return Playlist{}, err
	}
	p.ID, p.UserID = id, u.ID
	return p, nil
}

func (u User) GetPlaylist(ctx context.Context, id int) (Playlist, error) {
	if u.keys == nil {
		return Playlist{}, ErrLocked
	}
	var data []byte
	err := db.QueryRowContext(ctx, `SELECT data FROM playlists WHERE user_id = ? AND id = ?`, u.ID, id).Scan(&data)
	if err != nil {
		return Playlist{}, notFound(err)
	}
	return u.openPlaylist(id, data)
}

func (u User) GetPlaylists(ctx context.Context) ([]Playlist, error) {
	if u.keys == nil {
		return nil, ErrLocked
	}
	rows, err := db.QueryContext(ctx, `SELECT id, data FROM playlists WHERE user_id = ? ORDER BY id`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pp []Playlist
	for rows.Next() {
		var (
			id   int
			data []byte
		)
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		p, err := u.openPlaylist(id, data)
		if err != nil {
			return nil, err
		}
		pp = append(pp, p)
	}
	return pp, rows.Err()
}

func (u User) DeletePlaylist(ctx context.Context, id int) error {
	_, err := db.ExecContext(ctx, `DELETE FROM playlists WHERE user_id = ? AND id = ?`, u.ID, id)
	return err
}
