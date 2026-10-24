CREATE TABLE refunds (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	invoice_id TEXT NOT NULL,
	amount INTEGER NOT NULL,
	status TEXT NOT NULL,
	pull_payment_id TEXT NOT NULL DEFAULT '',
	link TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL
);

CREATE INDEX refunds_user ON refunds (user_id, at);
CREATE INDEX refunds_invoice ON refunds (invoice_id);
