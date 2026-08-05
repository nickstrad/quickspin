CREATE TABLE sandboxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL DEFAULT 1,
    platform_id TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL,
    spec TEXT NOT NULL CHECK(json_valid(spec)),
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE idempotency_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    sandbox_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(sandbox_id) REFERENCES sandboxes(platform_id) ON DELETE CASCADE
);

CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL,
    sandbox_id TEXT NOT NULL,
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    at DATETIME NOT NULL,
    reason TEXT NOT NULL,
    FOREIGN KEY(sandbox_id) REFERENCES sandboxes(platform_id)
);

CREATE UNIQUE INDEX events_sandbox_id_version_id ON events(sandbox_id, version_id);
