-- 0001_init.sql — Taskronin initial schema.
-- AIDEV-NOTE: committed migration; checksummed; NEVER edit. Add NNNN_*.sql instead.
-- AIDEV-NOTE: FTS external content is a VIEW of live rows (deleted_at IS NULL) so that
--   'rebuild' and content integrity-check agree with the trigger rules (see §14 issue 2).

-- ---------------------------------------------------------------- tasks
CREATE TABLE tasks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    title          TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    notes          TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(notes AS BLOB)) <= 65536),
    status         TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','dropped')),
    rank           INTEGER NOT NULL,
    last_placed_at INTEGER,
    last_placed_seq INTEGER,              -- ops.seq of the last placement (§4.2)
    defer_date     TEXT    CHECK (defer_date IS NULL OR defer_date IS strftime('%Y-%m-%d', defer_date)),
    defer_min      INTEGER CHECK (defer_min IS NULL OR defer_min BETWEEN 0 AND 1439),
    due_date       TEXT    CHECK (due_date IS NULL OR due_date IS strftime('%Y-%m-%d', due_date)),
    due_min        INTEGER CHECK (due_min IS NULL OR due_min BETWEEN 0 AND 1439),
    due_ack        TEXT    NOT NULL DEFAULT 'none' CHECK (due_ack IN ('none','due','overdue')),
    due_ack_date   TEXT    CHECK (due_ack_date IS NULL OR due_ack_date IS strftime('%Y-%m-%d', due_ack_date)),
    project        TEXT    CHECK (project IS NULL OR (length(project) BETWEEN 1 AND 100
                                  AND project NOT GLOB '*[^A-Za-z0-9._-]*')),
    priority       TEXT    CHECK (priority IS NULL OR priority IN ('H','M','L')),
    origin         TEXT    NOT NULL,
    completed_at   INTEGER,
    completed_date TEXT    CHECK (completed_date IS NULL OR completed_date IS strftime('%Y-%m-%d', completed_date)),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    deleted_at     INTEGER,
    purged_at      INTEGER,
    version        INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (defer_min IS NULL OR defer_date IS NOT NULL),
    CHECK (due_min IS NULL OR due_date IS NOT NULL),
    CHECK ((status = 'done') = (completed_at IS NOT NULL)),
    CHECK ((completed_at IS NULL) = (completed_date IS NULL))
) STRICT;
CREATE INDEX tasks_rank       ON tasks(rank, id);
CREATE INDEX tasks_open_rank  ON tasks(rank, id) WHERE status = 'open' AND deleted_at IS NULL;
CREATE INDEX tasks_defer      ON tasks(defer_date) WHERE defer_date IS NOT NULL;
CREATE INDEX tasks_due        ON tasks(due_date) WHERE due_date IS NOT NULL AND status = 'open';
CREATE INDEX tasks_completed  ON tasks(completed_date) WHERE completed_date IS NOT NULL;
CREATE INDEX tasks_project    ON tasks(project) WHERE project IS NOT NULL;

CREATE TABLE task_triage (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    reason  TEXT    NOT NULL CHECK (reason IN ('slipped','overdue','due','continue',
                                               'surfaced','reopened','manual','new')),
    since   INTEGER NOT NULL,             -- informational
    seq     INTEGER NOT NULL,             -- ops.seq that added it; ordering
    PRIMARY KEY (task_id, reason)
) STRICT, WITHOUT ROWID;
CREATE INDEX task_triage_reason ON task_triage(reason, seq);

CREATE TABLE tags (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE
               CHECK (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^A-Za-z0-9._-]*'),
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE task_tags (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE RESTRICT,
    PRIMARY KEY (task_id, tag_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX task_tags_tag ON task_tags(tag_id, task_id);

-- ---------------------------------------------------------------- log entries
CREATE TABLE log_entries (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER REFERENCES tasks(id) ON DELETE RESTRICT,
    kind         TEXT    NOT NULL CHECK (kind IN ('work','complete')),
    title        TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    notes        TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(notes AS BLOB)) <= 65536),
    work_date    TEXT    NOT NULL CHECK (work_date IS strftime('%Y-%m-%d', work_date)),
    occurred_min INTEGER CHECK (occurred_min IS NULL OR occurred_min BETWEEN 0 AND 1439),
    reopened     INTEGER NOT NULL DEFAULT 0 CHECK (reopened IN (0,1)),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    deleted_at   INTEGER,
    purged_at    INTEGER,
    version      INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (kind = 'work' OR task_id IS NOT NULL),
    CHECK (reopened = 0 OR kind = 'complete')
) STRICT;
CREATE INDEX log_entries_day  ON log_entries(work_date, occurred_min, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX log_entries_task ON log_entries(task_id, work_date) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------- time blocks
CREATE TABLE time_blocks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id        INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    date           TEXT    NOT NULL CHECK (date IS strftime('%Y-%m-%d', date)),
    start_min      INTEGER NOT NULL CHECK (start_min BETWEEN 0 AND 1439),
    end_min        INTEGER NOT NULL CHECK (end_min BETWEEN 1 AND 1440),
    status         TEXT    NOT NULL DEFAULT 'planned'
                   CHECK (status IN ('planned','done','worked','missed','cancelled')),
    outcome_source TEXT    CHECK (outcome_source IS NULL OR outcome_source IN ('auto','user')),
    cancel_reason  TEXT    CHECK (cancel_reason IS NULL
                                  OR cancel_reason IN ('user','task_closed','deferred','rescheduled')),
    slipped        INTEGER NOT NULL DEFAULT 0 CHECK (slipped IN (0,1)),
    slipped_at     INTEGER,
    slipped_seq    INTEGER,
    created_at     INTEGER NOT NULL,
    resolved_at    INTEGER,
    resolved_seq   INTEGER,
    deleted_at     INTEGER,
    version        INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (start_min < end_min),
    CHECK ((status IN ('done','worked','missed')) = (outcome_source IS NOT NULL)),
    CHECK ((status = 'cancelled') = (cancel_reason IS NOT NULL)),
    CHECK ((status = 'planned') = (resolved_at IS NULL)),
    CHECK (status <> 'done' OR outcome_source = 'auto'),
    CHECK (slipped = 0 OR (slipped_at IS NOT NULL AND slipped_seq IS NOT NULL)),
    CHECK ((status = 'planned') = (resolved_seq IS NULL))
) STRICT;
CREATE INDEX time_blocks_day     ON time_blocks(date, start_min) WHERE deleted_at IS NULL;
CREATE INDEX time_blocks_task    ON time_blocks(task_id, date);
CREATE INDEX time_blocks_planned ON time_blocks(date) WHERE status = 'planned' AND deleted_at IS NULL;

-- AIDEV-NOTE: backstop for invariant I2; the service checks first and reports a friendly error.
CREATE TRIGGER time_blocks_no_overlap_ins BEFORE INSERT ON time_blocks
WHEN NEW.status = 'planned' AND NEW.deleted_at IS NULL
BEGIN
    SELECT RAISE(ABORT, 'taskronin: overlapping planned block')
    WHERE EXISTS (SELECT 1 FROM time_blocks b
                  WHERE b.date = NEW.date AND b.status = 'planned' AND b.deleted_at IS NULL
                    AND b.start_min < NEW.end_min AND NEW.start_min < b.end_min);
END;
CREATE TRIGGER time_blocks_no_overlap_upd
BEFORE UPDATE OF date, start_min, end_min, status, deleted_at ON time_blocks
WHEN NEW.status = 'planned' AND NEW.deleted_at IS NULL
BEGIN
    SELECT RAISE(ABORT, 'taskronin: overlapping planned block')
    WHERE EXISTS (SELECT 1 FROM time_blocks b
                  WHERE b.id <> NEW.id AND b.date = NEW.date AND b.status = 'planned'
                    AND b.deleted_at IS NULL
                    AND b.start_min < NEW.end_min AND NEW.start_min < b.end_min);
END;

-- ---------------------------------------------------------------- focus, interruptions
CREATE TABLE focus (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    task_id INTEGER REFERENCES tasks(id),
    date    TEXT    CHECK (date IS NULL OR date IS strftime('%Y-%m-%d', date)),
    set_at  INTEGER,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK ((task_id IS NULL) = (date IS NULL))
) STRICT;
INSERT INTO focus (id) VALUES (1);

-- ---------------------------------------------------------------- ops, events, undo, idempotency
CREATE TABLE ops (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id         TEXT    NOT NULL UNIQUE CHECK (length(op_id) = 32 AND op_id NOT GLOB '*[^0-9a-f]*'),
    kind          TEXT    NOT NULL,
    actor         TEXT    NOT NULL,
    channel       TEXT    NOT NULL CHECK (channel IN ('cli','tui','web','mcp','api','system')),
    at            INTEGER NOT NULL,
    scopes        TEXT    NOT NULL,
    revertible    INTEGER NOT NULL CHECK (revertible IN (0,1)),
    state         TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','reverted')),
    reverts_op_id TEXT    REFERENCES ops(op_id),
    via           TEXT    CHECK (via IS NULL OR via IN ('revert','undo','redo')),
    purged        INTEGER NOT NULL DEFAULT 0 CHECK (purged IN (0,1)),
    CHECK ((via IS NULL) = (reverts_op_id IS NULL))
) STRICT;
CREATE INDEX ops_actor_at ON ops(actor, at);
CREATE INDEX ops_reverts  ON ops(reverts_op_id) WHERE reverts_op_id IS NOT NULL;

CREATE TABLE interruptions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            INTEGER NOT NULL,
    date          TEXT    NOT NULL CHECK (date IS strftime('%Y-%m-%d', date)),
    from_task_id  INTEGER REFERENCES tasks(id),
    from_block_id INTEGER REFERENCES time_blocks(id),
    to_task_id    INTEGER NOT NULL REFERENCES tasks(id),
    op_id         TEXT    NOT NULL REFERENCES ops(op_id),
    deleted_at    INTEGER,
    CHECK (from_task_id IS NOT NULL OR from_block_id IS NOT NULL)
) STRICT;
CREATE INDEX interruptions_day ON interruptions(date) WHERE deleted_at IS NULL;

CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id       TEXT    NOT NULL REFERENCES ops(op_id),
    entity      TEXT    NOT NULL CHECK (entity IN ('task','triage','task_tags','entry','block',
                                                   'focus','interruption','token','setting','auth')),
    entity_id   INTEGER NOT NULL,
    task_id     INTEGER REFERENCES tasks(id),
    action      TEXT    NOT NULL CHECK (action IN ('insert','update','move','tombstone','untombstone',
                                                   'add','remove','set','revoke','purge')),
    before_json TEXT    CHECK (before_json IS NULL OR json_valid(before_json)),
    after_json  TEXT    CHECK (after_json IS NULL OR json_valid(after_json)),
    derived     INTEGER NOT NULL DEFAULT 0 CHECK (derived IN (0,1))  -- written by recompute (§4.6)
) STRICT;
CREATE INDEX events_entity ON events(entity, entity_id, id);
CREATE INDEX events_task   ON events(task_id, id) WHERE task_id IS NOT NULL;
CREATE INDEX events_op     ON events(op_id, id);

CREATE TABLE undo_stack (
    actor TEXT    NOT NULL,
    stack TEXT    NOT NULL CHECK (stack IN ('undo','redo')),
    seq   INTEGER NOT NULL,
    op_id TEXT    NOT NULL REFERENCES ops(op_id),
    PRIMARY KEY (actor, stack, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE idempotency (
    actor        TEXT    NOT NULL,
    key          TEXT    NOT NULL CHECK (length(key) BETWEEN 16 AND 128 AND key NOT GLOB '*[^A-Za-z0-9_-]*'),
    request_hash BLOB    NOT NULL CHECK (length(request_hash) = 32),
    op_id        TEXT    REFERENCES ops(op_id),   -- NULL for a stored no-op or terminal error
    status_code  INTEGER NOT NULL CHECK (status_code IN (200, 201, 409)),
    entity_refs  TEXT    NOT NULL CHECK (json_valid(entity_refs)),
    error_json   TEXT    CHECK (error_json IS NULL OR json_valid(error_json)), -- 409 only: full problem body of a committed pop
    created_at   INTEGER NOT NULL,
    CHECK (op_id IS NOT NULL OR status_code IN (200, 409)),
    CHECK ((status_code = 409) = (error_json IS NOT NULL)),
    PRIMARY KEY (actor, key)
) STRICT, WITHOUT ROWID;
CREATE INDEX idempotency_created ON idempotency(created_at);

-- ---------------------------------------------------------------- settings and auth
-- settings keys: db_generation, tz_last_seen, backup_keep_daily, backup_keep_weekly,
--   last_backup_at, last_backup_attempt_at, last_backup_error, ai_reviewed_through.
--   Secrets live in auth_state (never exported).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
INSERT INTO settings (key, value) VALUES
    ('backup_keep_daily', '7'), ('backup_keep_weekly', '4'), ('ai_reviewed_through', '0'),
    ('last_backup_at', ''), ('last_backup_attempt_at', ''),
    ('last_backup_error', ''), ('tz_last_seen', '');

CREATE TABLE auth_state (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash       TEXT    NOT NULL CHECK (password_hash GLOB '$argon2id$*'),
    password_changed_at INTEGER NOT NULL,
    web_login_disabled  INTEGER NOT NULL DEFAULT 0 CHECK (web_login_disabled IN (0,1)),
    idem_hmac_key       BLOB    NOT NULL CHECK (length(idem_hmac_key) = 32)
) STRICT;

CREATE TABLE api_tokens (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE
                       CHECK (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^A-Za-z0-9._-]*'),
    secret_hash        BLOB    NOT NULL CHECK (length(secret_hash) = 32),
    scopes             TEXT    NOT NULL,
    is_owner           INTEGER NOT NULL DEFAULT 0 CHECK (is_owner IN (0,1)),
    mutations_per_hour INTEGER NOT NULL DEFAULT 0 CHECK (mutations_per_hour >= 0),
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER,
    last_used_at       INTEGER,
    revoked_at         INTEGER,
    CHECK ((is_owner = 1) = (name GLOB 'owner.*'))
) STRICT;
CREATE UNIQUE INDEX api_tokens_one_owner ON api_tokens(is_owner) WHERE is_owner = 1 AND revoked_at IS NULL;

CREATE TABLE web_sessions (
    id_hash      BLOB    PRIMARY KEY CHECK (length(id_hash) = 32),
    csrf_secret  BLOB    NOT NULL CHECK (length(csrf_secret) = 32),
    remote       TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,   -- absolute (created_at + 30 d)
    last_seen_at INTEGER NOT NULL    -- idle limit 7 d, flushed ≤ 1/min
) STRICT, WITHOUT ROWID;
CREATE INDEX web_sessions_expires ON web_sessions(expires_at);

-- ---------------------------------------------------------------- full-text search
CREATE VIEW tasks_fts_src AS
    SELECT id, title, notes FROM tasks WHERE deleted_at IS NULL;
CREATE VIRTUAL TABLE tasks_fts USING fts5(
    title, notes, content='tasks_fts_src', content_rowid='id',
    tokenize='unicode61 remove_diacritics 2');
INSERT INTO tasks_fts (tasks_fts, rank) VALUES ('secure-delete', 1);
CREATE TRIGGER tasks_fts_ins AFTER INSERT ON tasks WHEN NEW.deleted_at IS NULL
BEGIN
    INSERT INTO tasks_fts (rowid, title, notes) VALUES (NEW.id, NEW.title, NEW.notes);
END;
CREATE TRIGGER tasks_fts_upd AFTER UPDATE OF title, notes, deleted_at ON tasks
BEGIN
    INSERT INTO tasks_fts (tasks_fts, rowid, title, notes)
        SELECT 'delete', OLD.id, OLD.title, OLD.notes WHERE OLD.deleted_at IS NULL;
    INSERT INTO tasks_fts (rowid, title, notes)
        SELECT NEW.id, NEW.title, NEW.notes WHERE NEW.deleted_at IS NULL;
END;

CREATE VIEW log_entries_fts_src AS
    SELECT id, title, notes FROM log_entries WHERE deleted_at IS NULL;
CREATE VIRTUAL TABLE log_entries_fts USING fts5(
    title, notes, content='log_entries_fts_src', content_rowid='id',
    tokenize='unicode61 remove_diacritics 2');
INSERT INTO log_entries_fts (log_entries_fts, rank) VALUES ('secure-delete', 1);
CREATE TRIGGER log_entries_fts_ins AFTER INSERT ON log_entries WHEN NEW.deleted_at IS NULL
BEGIN
    INSERT INTO log_entries_fts (rowid, title, notes) VALUES (NEW.id, NEW.title, NEW.notes);
END;
CREATE TRIGGER log_entries_fts_upd AFTER UPDATE OF title, notes, deleted_at ON log_entries
BEGIN
    INSERT INTO log_entries_fts (log_entries_fts, rowid, title, notes)
        SELECT 'delete', OLD.id, OLD.title, OLD.notes WHERE OLD.deleted_at IS NULL;
    INSERT INTO log_entries_fts (rowid, title, notes)
        SELECT NEW.id, NEW.title, NEW.notes WHERE NEW.deleted_at IS NULL;
END;

-- ---------------------------------------------------------------- guard triggers
CREATE TRIGGER tasks_no_delete         BEFORE DELETE ON tasks         BEGIN SELECT RAISE(ABORT, 'taskronin: tasks are tombstoned, never deleted'); END;
CREATE TRIGGER log_entries_no_delete   BEFORE DELETE ON log_entries   BEGIN SELECT RAISE(ABORT, 'taskronin: entries are tombstoned, never deleted'); END;
CREATE TRIGGER time_blocks_no_delete   BEFORE DELETE ON time_blocks   BEGIN SELECT RAISE(ABORT, 'taskronin: blocks are tombstoned, never deleted'); END;
CREATE TRIGGER interruptions_no_delete BEFORE DELETE ON interruptions BEGIN SELECT RAISE(ABORT, 'taskronin: interruptions are tombstoned, never deleted'); END;
CREATE TRIGGER ops_no_delete           BEFORE DELETE ON ops           BEGIN SELECT RAISE(ABORT, 'taskronin: ops are append-only'); END;
CREATE TRIGGER events_no_delete        BEFORE DELETE ON events        BEGIN SELECT RAISE(ABORT, 'taskronin: events are append-only'); END;
-- Only purge may rewrite before_json/after_json (content scrub); identity columns are frozen.
CREATE TRIGGER events_frozen BEFORE UPDATE ON events
WHEN NEW.id IS NOT OLD.id OR NEW.op_id IS NOT OLD.op_id OR NEW.entity IS NOT OLD.entity
  OR NEW.entity_id IS NOT OLD.entity_id OR NEW.task_id IS NOT OLD.task_id OR NEW.action IS NOT OLD.action
  OR NEW.derived IS NOT OLD.derived
BEGIN
    SELECT RAISE(ABORT, 'taskronin: events are append-only');
END;
CREATE TRIGGER log_entries_kind_fixed BEFORE UPDATE OF kind ON log_entries
WHEN NEW.kind IS NOT OLD.kind
BEGIN
    SELECT RAISE(ABORT, 'taskronin: entry kind is immutable');
END;
CREATE TRIGGER log_entries_complete_bound BEFORE UPDATE OF task_id ON log_entries
WHEN OLD.kind = 'complete' AND NEW.task_id IS NOT OLD.task_id
BEGIN
    SELECT RAISE(ABORT, 'taskronin: complete entries cannot be relinked');
END;
CREATE TRIGGER time_blocks_task_fixed BEFORE UPDATE OF task_id ON time_blocks
WHEN NEW.task_id IS NOT OLD.task_id
BEGIN
    SELECT RAISE(ABORT, 'taskronin: block task is immutable');
END;
