package tube

import "context"

type Totals struct {
	Users  int
	Usage  int64
	Tracks int
}

func SumUsers(users []User) Totals {
	var t Totals
	for _, u := range users {
		t.Users++
		t.Usage += u.Usage
		t.Tracks += u.Tracks
	}
	return t
}

func DeleteUser(ctx context.Context, userID int) error {
	res, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
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
