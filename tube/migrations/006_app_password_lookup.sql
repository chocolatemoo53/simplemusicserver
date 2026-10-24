ALTER TABLE app_passwords ADD COLUMN lookup TEXT NOT NULL DEFAULT '';

CREATE INDEX app_passwords_lookup ON app_passwords (lookup);
