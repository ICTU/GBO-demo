CREATE TABLE source_candidates (
    source_id text PRIMARY KEY,
    metadata_version text NOT NULL,
    metadata_payload_digest text NOT NULL CHECK (metadata_payload_digest ~ '^[0-9a-f]{64}$'),
    metadata_etag text NOT NULL DEFAULT '',
    deployment_digest text NOT NULL CHECK (deployment_digest ~ '^[0-9a-f]{64}$'),
    checked_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    fresh_until timestamptz NOT NULL,
    stale_until timestamptz NOT NULL,
    transport_authenticated boolean NOT NULL,
    snapshot jsonb NOT NULL,
    offers jsonb NOT NULL,
    certificate_set jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (fresh_until <= stale_until AND stale_until <= expires_at)
);

CREATE TABLE source_candidate_type_metadata (
    source_id text NOT NULL REFERENCES source_candidates(source_id) ON DELETE CASCADE,
    vct text NOT NULL,
    type_version text NOT NULL,
    integrity text NOT NULL,
    media_type text NOT NULL,
    bytes bytea NOT NULL,
    PRIMARY KEY (source_id, vct, type_version)
);

CREATE TABLE source_statuses (
    source_id text PRIMARY KEY,
    state text NOT NULL CHECK (state IN ('pending', 'active', 'stale', 'blocked', 'rollout_required')),
    reason text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    metadata_version text NOT NULL DEFAULT '',
    deployment_digest text NOT NULL DEFAULT '',
    transport_authenticated boolean NOT NULL,
    checked_at timestamptz NOT NULL
);

CREATE TABLE source_releases (
    release_id text PRIMARY KEY CHECK (release_id ~ '^[0-9a-f]{64}$'),
    digest text NOT NULL UNIQUE CHECK (digest ~ '^[0-9a-f]{64}$'),
    materialization_digest text NOT NULL CHECK (materialization_digest ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    offers jsonb NOT NULL
);

CREATE TABLE source_release_sources (
    release_id text NOT NULL REFERENCES source_releases(release_id) ON DELETE CASCADE,
    source_id text NOT NULL,
    metadata_version text NOT NULL,
    metadata_payload_digest text NOT NULL CHECK (metadata_payload_digest ~ '^[0-9a-f]{64}$'),
    metadata_etag text NOT NULL DEFAULT '',
    deployment_digest text NOT NULL CHECK (deployment_digest ~ '^[0-9a-f]{64}$'),
    checked_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    fresh_until timestamptz NOT NULL,
    stale_until timestamptz NOT NULL,
    transport_authenticated boolean NOT NULL,
    snapshot jsonb NOT NULL,
    certificate_set jsonb NOT NULL,
    PRIMARY KEY (release_id, source_id),
    CHECK (fresh_until <= stale_until AND stale_until <= expires_at)
);

CREATE TABLE source_release_type_metadata (
    release_id text NOT NULL,
    source_id text NOT NULL,
    vct text NOT NULL,
    type_version text NOT NULL,
    integrity text NOT NULL,
    media_type text NOT NULL,
    bytes bytea NOT NULL,
    PRIMARY KEY (release_id, source_id, vct, type_version),
    FOREIGN KEY (release_id, source_id)
        REFERENCES source_release_sources(release_id, source_id) ON DELETE CASCADE
);

CREATE TABLE active_source_release (
    singleton smallint PRIMARY KEY DEFAULT 1 CHECK (singleton = 1),
    release_id text NOT NULL REFERENCES source_releases(release_id)
);

CREATE INDEX source_candidates_freshness_idx
    ON source_candidates (stale_until, source_id);

CREATE INDEX source_statuses_state_idx
    ON source_statuses (state, checked_at DESC);

CREATE INDEX source_releases_created_idx
    ON source_releases (created_at DESC);
