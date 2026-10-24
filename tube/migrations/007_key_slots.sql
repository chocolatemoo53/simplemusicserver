DROP TABLE sessions;

CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	expires INTEGER NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	wrapped BLOB NOT NULL
) WITHOUT ROWID;

CREATE INDEX sessions_expires ON sessions (expires);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE key_slots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	salt BLOB,
	wrapped BLOB NOT NULL,
	created INTEGER NOT NULL,
	UNIQUE (user_id, kind)
);
