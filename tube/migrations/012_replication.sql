CREATE TABLE replicas (
	key TEXT PRIMARY KEY,
	state TEXT NOT NULL,
	retain_until INTEGER NOT NULL DEFAULT 0,
	next_at INTEGER NOT NULL DEFAULT 0,
	lease_until INTEGER NOT NULL DEFAULT 0,
	attempts INTEGER NOT NULL DEFAULT 0,
	verified INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE INDEX replicas_due ON replicas (next_at) WHERE state != 'copied';
CREATE INDEX replicas_retain ON replicas (retain_until) WHERE state = 'copied';
CREATE INDEX replicas_verified ON replicas (verified) WHERE state = 'copied';
