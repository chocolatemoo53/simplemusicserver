DROP TABLE app_passwords;

CREATE TABLE app_passwords (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	strict INTEGER NOT NULL DEFAULT 0,
	secret BLOB,
	lookup TEXT NOT NULL UNIQUE,
	wrapped BLOB NOT NULL,
	created INTEGER NOT NULL,
	last_used INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX app_passwords_user ON app_passwords (user_id);
