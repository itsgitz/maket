CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  github_id INTEGER UNIQUE NOT NULL,
  github_login TEXT NOT NULL,
  name TEXT,
  avatar_url TEXT,
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE sessions (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash BLOB UNIQUE NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE api_keys (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  key_hash BLOB UNIQUE NOT NULL,
  last_used_at TIMESTAMP,
  revoked_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE prototypes (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  slug TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  spa INTEGER NOT NULL DEFAULT 0,
  current_version INTEGER NOT NULL,
  size_bytes INTEGER NOT NULL,
  file_count INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);

CREATE INDEX sessions_expires_at ON sessions(expires_at);
CREATE INDEX sessions_user_id ON sessions(user_id);
CREATE INDEX api_keys_user_id ON api_keys(user_id);
CREATE INDEX prototypes_user_id ON prototypes(user_id);
