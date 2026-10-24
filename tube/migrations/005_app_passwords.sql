CREATE TABLE app_passwords (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	secret BLOB NOT NULL,
	created INTEGER NOT NULL,
	last_used INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX app_passwords_user ON app_passwords (user_id);
