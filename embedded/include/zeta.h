/**
 * zeta.h — C API for the Zeta embedded database
 *
 * A SQLite-inspired interface over the Zeta engine. Zeta supports ACID
 * transactions, JSONB, full-text search, vector similarity, and full
 * PostgreSQL-compatible SQL.
 *
 * Typical usage:
 *
 *   zeta_db_t *db = zeta_open(":memory:", NULL);
 *   zeta_exec(db, "CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT)", NULL);
 *
 *   zeta_stmt_t *stmt = zeta_prepare(db, "INSERT INTO kv VALUES ($1, $2)", NULL);
 *   zeta_bind_text(stmt, 1, "hello", -1);
 *   zeta_bind_text(stmt, 2, "world", -1);
 *   zeta_step(stmt);
 *   zeta_finalize(stmt);
 *
 *   stmt = zeta_prepare(db, "SELECT v FROM kv WHERE k = $1", NULL);
 *   zeta_bind_text(stmt, 1, "hello", -1);
 *   if (zeta_step(stmt) == ZETA_ROW)
 *       printf("%s\n", zeta_column_text(stmt, 0));
 *   zeta_finalize(stmt);
 *
 *   zeta_close(db);
 */

#ifndef ZETA_H
#define ZETA_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ── Return codes ──────────────────────────────────────────────────────────── */

/** Successful result. */
#define ZETA_OK        0
/** zeta_step() has a row ready. */
#define ZETA_ROW       1
/** zeta_step() has finished iterating rows. */
#define ZETA_DONE    100

/** SQL parse / syntax error. */
#define ZETA_ERR_PARSE       (-1)
/** Type mismatch. */
#define ZETA_ERR_TYPE        (-2)
/** Constraint violation (PK, unique, FK, check). */
#define ZETA_ERR_CONSTRAINT  (-3)
/** Transaction conflict (serialization failure). */
#define ZETA_ERR_CONFLICT    (-4)
/** Row or object not found. */
#define ZETA_ERR_NOT_FOUND   (-5)
/** Storage / I/O error. */
#define ZETA_ERR_STORAGE     (-6)
/**
 * Returned by zeta_olap_stats() when no OLAP engine is running (the database
 * was opened without an analytical backend). Distinct from ZETA_OK with zero
 * counters, which means the engine IS running but has served no analytical
 * queries yet.
 */
#define ZETA_OLAP_ABSENT     (-7)
/** Unknown / unclassified error. */
#define ZETA_ERR_UNKNOWN     (-99)

/* ── Storage backend selector (zeta_open_mode) ─────────────────────────────── */

/**
 * Backend codes for zeta_open_mode(). Plain zeta_open() uses ZETA_MODE_QUARTZ
 * with the OLTP profile, which does NOT start the OLAP columnar engine.
 * Analytics are opt-in: OR ZETA_MODE_FLAG_HTAP into any mode, e.g.
 * (ZETA_MODE_QUARTZ | ZETA_MODE_FLAG_HTAP), to start the columnar tier. (The
 * flag only has an effect in a build compiled with the `olap` feature.)
 *
 * ZETA_MODE_BTREE (0) is the retired BTree backend. The value stays reserved
 * so old callers fail loudly rather than receive a different engine:
 * zeta_open_mode() rejects it with an error. A data directory written by the
 * BTree backend cannot be opened by any remaining backend; open it with the
 * last release that shipped BTree.
 */
#define ZETA_MODE_BTREE   0   /* removed -- rejected */
#define ZETA_MODE_LSM     1
#define ZETA_MODE_QUARTZ  2
#define ZETA_MODE_MEMORY  3
/** Flag bit, OR-ed into a mode: also start the columnar analytics (OLAP) tier. */
#define ZETA_MODE_FLAG_HTAP 0x100

/*
 * How correlated subqueries run (the engine's ZETA_SUBPLAN setting). Without
 * either flag below the engine's default applies: the ZETA_SUBPLAN environment
 * variable when the process sets one, otherwise the built-in mode. Either flag
 * PINS the mode for this database and the environment variable is ignored, so
 * a host that has no say over its process environment can still choose. Set at
 * most one: both together is an error (NULL and a message in err_msg, nothing
 * created), as is any other unknown flag bit.
 *
 * The flags go to the calls that take a mode: zeta_open_mode(),
 * zeta_open_with_retention() and zeta_open_with_options(). zeta_open() and
 * zeta_open_memory() take no mode and always use the engine's default; to pin
 * the mode use zeta_open_mode(path, ZETA_MODE_QUARTZ | flag) for what
 * zeta_open(path) opens, and zeta_open_mode(NULL, ZETA_MODE_MEMORY | flag) for
 * an in-memory database (the same SQL as zeta_open_memory(), on the engine
 * that does not use a data directory).
 *
 * Compatibility: a library that predates these flags rejects them with an
 * "unknown bits" error (a NULL handle). A host that always passes
 * ZETA_MODE_FLAG_SUBPLAN_OFF must treat that error as "this library predates
 * the flag" and decide what to do (open without it, or refuse). zeta_version()
 * returns the library's crate version string; it is not a capability query.
 */

/**
 * Flag bit, OR-ed into a mode: subplan mode OFF. Correlated subqueries run the
 * way they did before the scoped-binder work (#4773): no binder tags, the
 * executor decides which names belong to an enclosing query, and a correlated
 * subquery inside a CASE branch (or in an AND / OR / COALESCE operand that a
 * decided left side would skip) is run for every row, so SELECT CASE WHEN
 * o.x <> 0 THEN (SELECT 1 / o.x ...) ELSE -1 END raises "division by zero" on
 * the rows with x = 0. This is the off switch. In a process that also holds a
 * database opened with mode ON, an OFF database pays a small speed cost (the
 * engine keeps one process-wide flag that the ON database sets); its answers
 * are unchanged.
 */
#define ZETA_MODE_FLAG_SUBPLAN_OFF 0x200
/**
 * Flag bit, OR-ed into a mode: subplan mode ON. Binder tags decide which names
 * are outer, a correlated subquery is lowered late into a prepared body, and
 * CASE / AND / OR / COALESCE evaluate lazily around it: a branch that is not
 * taken does not run its subquery (the PostgreSQL behaviour), so the statement
 * above returns -1 for the rows with x = 0.
 */
#define ZETA_MODE_FLAG_SUBPLAN_ON  0x400

/* ── Column type codes ─────────────────────────────────────────────────────── */

/*
 * Values returned by zeta_column_type(). 0-4 are the SQLite-style classes;
 * 5 and up are Zeta extensions with no SQLite equivalent.
 *
 * Every code is readable through zeta_column_text(): the typed codes 6-9 add
 * no accessor of their own, they say what the text *means*. Codes 10-13 are
 * also readable through zeta_column_int64(). The list may grow;
 * treat any code you do not recognise as text and read it with
 * zeta_column_text() rather than as NULL.
 */
#define ZETA_TYPE_NULL     0
#define ZETA_TYPE_INT      1
#define ZETA_TYPE_FLOAT    2
#define ZETA_TYPE_TEXT     3
#define ZETA_TYPE_BLOB     4
/** SQL BOOLEAN. Read with zeta_column_int64() — returns 0 (false) or 1 (true). */
#define ZETA_TYPE_BOOL     5
/**
 * SQL NUMERIC / DECIMAL. zeta_column_text() yields the decimal string in the
 * scale the engine holds — "3.50" from DECIMAL(10,2) — not the shortest float
 * that round-trips. zeta_column_int64() / zeta_column_double() return 0.
 */
#define ZETA_TYPE_DECIMAL  6
/** SQL UUID, as lowercase hyphenated text. */
#define ZETA_TYPE_UUID     7
/**
 * SQL JSONB, and any SQL array (rendered as a JSON array). Compact JSON text.
 * A JSONB value nested more than 127 containers deep reports ZETA_TYPE_TEXT
 * instead, carrying the same JSON text.
 */
#define ZETA_TYPE_JSON     8
/** SQL VECTOR(n), as the bracketed literal SQL uses: "[0.5,0.25]". */
#define ZETA_TYPE_VECTOR   9
/*
 * Temporal types. Unlike 6-9 these also answer zeta_column_int64() (and
 * zeta_column_double()) with the raw payload given below; zeta_column_text()
 * yields ISO 8601. An archive older than these codes reports ZETA_TYPE_INT for
 * all four with the same integer payload, so a reader that followed the
 * "unknown code → text" rule above now gets ISO text where it used to get a
 * number.
 */
/** SQL TIMESTAMP: µs since 1970-01-01T00:00:00. "2026-01-15T12:34:56.500000". */
#define ZETA_TYPE_TIMESTAMP   10
/** SQL TIMESTAMPTZ: µs since the Unix epoch, UTC. "2026-01-15T10:34:56.000000+00:00". */
#define ZETA_TYPE_TIMESTAMPTZ 11
/** SQL DATE: days since 1970-01-01. "2026-01-15". */
#define ZETA_TYPE_DATE        12
/** SQL TIME: µs since midnight. "12:34:56.250000". */
#define ZETA_TYPE_TIME        13

/* ── Opaque handle types ───────────────────────────────────────────────────── */

/** Database handle. Obtain with zeta_open / zeta_open_memory. */
typedef struct ZetaDb   zeta_db_t;
/** Prepared statement handle. Obtain with zeta_prepare. */
typedef struct ZetaStmt zeta_stmt_t;
/** Transaction handle. Obtain with zeta_begin. */
typedef struct ZetaTxn  zeta_txn_t;

/* ── Database lifecycle ────────────────────────────────────────────────────── */

/**
 * Open a persistent database at `path`.
 *
 * Pass ":memory:" for an in-memory database (SQLite convention).
 * On failure returns NULL; if `err_msg` is non-NULL it is set to a
 * newly-allocated error string — free with zeta_free().
 *
 * The write-ahead log is reclaimed automatically (about the newest 256 MiB is
 * kept; nothing a restart needs is ever deleted); there is nothing to configure.
 * See zeta_open_with_retention() only to change that.
 *
 * Always uses the engine's default subplan mode (see
 * ZETA_MODE_FLAG_SUBPLAN_OFF): zeta_open(path) is zeta_open_mode(path,
 * ZETA_MODE_QUARTZ), so to choose the mode open with zeta_open_mode(path,
 * ZETA_MODE_QUARTZ | flag). The ":memory:" shorthand is zeta_open_memory(),
 * which cannot be pinned either: use zeta_open_mode(NULL, ZETA_MODE_MEMORY |
 * flag) for an in-memory database whose mode you choose.
 */
zeta_db_t *zeta_open(const char *path, char **err_msg);

/**
 * Open a pure in-memory database. Data is lost when the handle is closed.
 *
 * Always uses the engine's default subplan mode (see ZETA_MODE_FLAG_SUBPLAN_OFF):
 * to choose the mode of an in-memory database, open it with
 * zeta_open_mode(NULL, ZETA_MODE_MEMORY | flag) instead.
 */
zeta_db_t *zeta_open_memory(char **err_msg);

/**
 * Open a database with an explicit storage backend (see ZETA_MODE_*).
 *
 * zeta_open() selects ZETA_MODE_QUARTZ with the OLTP profile, which does NOT
 * start the OLAP columnar engine. OR ZETA_MODE_FLAG_HTAP into the mode to use
 * analytical queries (OLAP/HTAP).
 *
 * A pure columnar table — CREATE TABLE t (...) WITH (table_kind='columnar') —
 * needs the same thing, and more: the columnar store is its ONLY store, so it
 * exists only where one was built. Without ZETA_MODE_FLAG_HTAP (including plain
 * zeta_open) the CREATE is rejected rather than accepted-then-answered-empty;
 * with the flag it is accepted. A columnar table also has no row-store machinery, so
 * CREATE INDEX / FOREIGN KEY / UNIQUE / triggers on it are rejected, and
 * reading it from the same statement's own transaction is refused (the tier is
 * materialized at COMMIT). See docs/design/pure_columnar_table.md.
 *
 * OR ZETA_MODE_FLAG_SUBPLAN_OFF or ZETA_MODE_FLAG_SUBPLAN_ON into the mode to
 * pin how correlated subqueries run (see their definitions); without either the
 * engine's default applies.
 *
 * `path` must be non-NULL for the persistent backends (LSM/QUARTZ); it is
 * ignored for ZETA_MODE_MEMORY. An unknown mode or flag bit is rejected, and so
 * is ZETA_MODE_BTREE, and so is setting both subplan flags. On failure returns
 * NULL and, if `err_msg` is non-NULL, sets it to a newly-allocated string —
 * free with zeta_free().
 */
zeta_db_t *zeta_open_mode(const char *path, int mode, char **err_msg);

/**
 * zeta_open_mode() with an explicit write-ahead-log retention (#4183).
 *
 * YOU NORMALLY DO NOT NEED THIS. zeta_open() and zeta_open_mode() already
 * reclaim the log automatically: they keep about the newest 256 MiB of log and
 * delete older segments once every consumer of the log (the row store's
 * checkpoint, the columnar and full-text appliers, replication slots) has
 * durably moved past them, so nothing a restart needs is ever deleted. Without
 * this the log would grow with every write (a row rewritten a thousand times is
 * a thousand log entries) and resident memory and reopen time with it. The
 * database checkpoints itself when that is what holds the log back (at most once
 * per 30 s), so the host never calls a checkpoint function.
 *
 * Call this only to change the tail or to keep the whole log:
 *   log_retention_max_mb       0 = the default (256 MiB);
 *                              UINT64_MAX = no cap, keep the whole log forever;
 *                              otherwise that many MiB. Keep it well above 64:
 *                              the active segment is never deleted, so the log
 *                              can overshoot the cap by up to one segment.
 *   log_retention_max_age_secs 0 = no age cap; otherwise sealed segments older
 *                              than that are also eligible (either cap alone is
 *                              enough to delete a segment).
 * (0, 0) is exactly zeta_open_mode(). What reclaiming gives up is replay of
 * entries older than the kept tail (point-in-time recovery, a replication
 * consumer that has not consumed them). Replication slots of an embedded wire
 * listener (zeta_start_pgwire and friends) are kept in memory
 * where retention cannot see them, so a CDC client connected to one is NOT held
 * back: such a host should pass UINT64_MAX. Ignored for ZETA_MODE_MEMORY. Same
 * path/mode/err_msg contract as zeta_open_mode().
 */
zeta_db_t *zeta_open_with_retention(const char *path, int mode,
                                    uint64_t log_retention_max_mb,
                                    uint64_t log_retention_max_age_secs,
                                    char **err_msg);

/**
 * Tuning knobs for zeta_open_with_options(). Zero-initialise the struct, set
 * struct_size = sizeof(zeta_open_options_t), then set only what you want to
 * change: EVERY OTHER FIELD'S ZERO VALUE MEANS "THE DEFAULT", so a struct with
 * only struct_size set opens exactly like zeta_open_mode(). struct_size lets a
 * later library version append fields without breaking a caller built against
 * this layout; a caller built against a NEWER layout that sets a field this
 * library does not know gets an error rather than a silently dropped setting.
 * A struct_size below 4, above 4096, or one that ends inside a field, is
 * rejected.
 */
typedef struct zeta_open_options {
    /** sizeof(zeta_open_options_t) as the caller compiled it. */
    uint32_t struct_size;
    /**
     * ZETA_MODE_QUARTZ only. Percentage (1-100) of the buffer pool kept as
     * decoded (uncompressed) pages; 0 = the default (ZETA_DEFAULT_DECODED_TIER_PCT).
     * A page outside the decoded tier is decompressed when it is read. Above 100
     * is rejected.
     */
    uint32_t decoded_tier_pct;
    /**
     * ZETA_MODE_QUARTZ only. Buffer-pool size in MiB; 0 = the default
     * (ZETA_DEFAULT_FRAME_BUDGET_MB).
     * A cap, not a reservation: nothing is allocated up front, and resident
     * memory grows with the pages you touch, up to this size. Above 1048576
     * (1 TiB), or more than the platform can address, is rejected. See "Sizing the
     * pool" below.
     */
    uint64_t frame_budget_mb;
    /** As zeta_open_with_retention(): 0 = default (256 MiB), UINT64_MAX = keep all. */
    uint64_t log_retention_max_mb;
    /** As zeta_open_with_retention(): 0 = no age cap. */
    uint64_t log_retention_max_age_secs;
    /**
     * ZETA_MODE_QUARTZ and ZETA_MODE_LSM. Whether a background monitor divides a
     * growing table into more primary-key ranges (a range splits once it passes
     * 4 GiB) and merges small neighbours back. ZETA_AUTO_SPLIT_DEFAULT (0) is the
     * embedded default, which is OFF in this preview. ZETA_AUTO_SPLIT_ON (1) turns
     * it on. ZETA_AUTO_SPLIT_OFF (2) pins it off, so a program that must stay off
     * keeps working if a later version turns the default on. Any other value is
     * rejected, and so is ON with ZETA_MODE_MEMORY (it has nothing to divide).
     * KNOWN LIMITATION while it is on: a scan that overlaps one of the monitor's
     * short split steps can miss rows, with no error. That covers a SELECT, and
     * also an UPDATE or DELETE that finds its rows by scanning (a WHERE that is
     * not a primary-key lookup), which can then touch fewer rows than match. It
     * was seen rarely (about 2 scans in 18,000, while a split step ran), and the
     * rows are still stored: a repeated scan usually, and a primary-key read, finds
     * them. Crash safety, secondary-index and columnar answers were tested with the
     * monitor on and held; those tests ran on ZETA_MODE_QUARTZ only. The known
     * limitation is why it is off by default. With it off a table stays one
     * primary-key range however large it grows; that case is not yet tested at
     * several GiB.
     */
    uint64_t auto_split;
} zeta_open_options_t;

/** The pool defaults a zero field stands for (ZETA_MODE_QUARTZ). */
#define ZETA_DEFAULT_FRAME_BUDGET_MB   512
#define ZETA_DEFAULT_DECODED_TIER_PCT  25

/** Values of zeta_open_options_t.auto_split. */
#define ZETA_AUTO_SPLIT_DEFAULT  0
#define ZETA_AUTO_SPLIT_ON       1
#define ZETA_AUTO_SPLIT_OFF      2

/**
 * zeta_open_mode() with tuning knobs: the Quartz buffer pool, the log
 * retention and the auto-split monitor. opts == NULL is exactly
 * zeta_open_mode() (the monitor is off). A pool field with
 * ZETA_MODE_LSM or ZETA_MODE_MEMORY is an error, not a silent no-op; the
 * retention fields are ignored for ZETA_MODE_MEMORY. The pool is sized per open
 * (the same directory can be reopened with another size); the page size of an
 * existing store is kept. Same path/mode/err_msg contract as zeta_open_mode().
 *
 * Sizing the pool. The default (512 MiB, of which 25% = 128 MiB is decoded)
 * serves point reads from memory while the hot pages fit in the decoded tier; a
 * read that misses it decompresses a page. (It was 64 MiB before #5114.) The
 * pool is a cap, not a reservation, and it is per open database (each
 * zeta_open has its own): resident memory grows with the pages the reads touch. Measured on one machine, a persistent store of ~700-byte rows (a
 * 128-dimension vector column) with one secondary index created after the bulk
 * load, prepared primary-key reads at random ids in a fresh process, median of
 * three interleaved runs (resident memory after the reads in brackets):
 *
 *   rows      64 MiB          256 MiB         512 MiB (default)  1024 MiB
 *   20 000    2.9 us (56)     1.33 us (62)    1.34 us (62)       1.34 us (63)
 *   100 000   6.7 us (100)    4.2 us (148)    1.65 us (196)      1.64 us (195)
 *   300 000   9.5 us (115)    6.9 us (260)    5.7 us (324)       3.45 us (453)
 *   500 000   10.1 us (117)   8.5 us (338)    6.8 us (438)       5.4 us (566)
 *
 * So a bigger pool is paid for in resident memory, in step with the data the
 * reads touch. At 100 000 rows the decoded tier (frame_budget_mb *
 * decoded_tier_pct / 100) needed between 64 and 128 MiB, i.e. 0.7-1.3 KB per
 * row, under uniform random reads (a skewed workload would need less; not
 * measured). Lower frame_budget_mb if the footprint matters more to you than
 * reads on a table of tens of thousands of rows or more; raise it (1024 MiB is
 * the server's default) for tables of several hundred thousand rows.
 * Your rows are not these rows: measure. Setup, raw output and the probe:
 * docs/embedded_pool_5114_runs.md in the engine repository.
 *
 * Not expressible here: the engine's degraded mode (decoded tier 0%), since a
 * zero field means the default.
 */
zeta_db_t *zeta_open_with_options(const char *path, int mode,
                                  const zeta_open_options_t *opts,
                                  char **err_msg);

/**
 * Open a new SESSION (connection) over the same engine as `db`.
 *
 * The embedded counterpart of duckdb_connect() over one duckdb_database: the
 * storage engine and runtime are shared, but the returned handle owns its
 * SESSION STATE — branch selection (zeta_set_branch), database selection, the
 * error slot and the changes counter — starting at the defaults (main branch).
 *
 * Session state is per connection, as in SQLite (each sqlite3* carries its own
 * per-connection PRAGMAs) and in Zeta's own pgwire (SET zeta_branch is keyed
 * per connection). Handing ONE handle to two concurrent workers therefore gives
 * them one shared branch pointer, and one worker's zeta_set_branch silently
 * retargets the other's statements. Give each concurrent worker its own:
 *
 *   zeta_db_t *db = zeta_open(path, &err);
 *   zeta_db_t *a  = zeta_connect(db, &err);
 *   zeta_db_t *b  = zeta_connect(db, &err);
 *   zeta_set_branch(a, "agent_a", &err);
 *   zeta_set_branch(b, "agent_b", &err);   // does not disturb a
 *
 * Like a DuckDB connection, one session is thread-safe but serialized while
 * querying, so prefer one session per thread.
 *
 * Close each session with zeta_close(); a session owns no wire listeners, so
 * closing it never disturbs listeners started on the zeta_open handle. Close
 * that handle last — it owns the engine every session borrows.
 *
 * Returns NULL (and sets err_msg, if non-NULL) when `db` is NULL.
 */
zeta_db_t *zeta_connect(zeta_db_t *db, char **err_msg);

/**
 * Select the database branch that the following statements on `db` run against.
 *
 * `name` is a branch created with `CREATE BRANCH <name>`. Branch DDL (CREATE, MERGE, DROP, SHOW BRANCH)
 * runs through zeta_exec and needs no selection, but it must run on a handle that has NONE selected: on a
 * handle that has selected a branch it fails ("branch DDL must run on main"), so create and merge through
 * the zeta_open handle, or clear the selection first. Pass NULL or "" to return to `main`.
 *
 * The selection is SESSION STATE: it is sticky on this handle and does not touch any other handle (give
 * each concurrent worker its own zeta_connect() session). It is shared with the statements prepared from
 * this handle, before or after this call: they all follow the handle's CURRENT selection.
 *
 * The name is stored, not validated: it is re-resolved to a fresh snapshot on every statement, so a branch
 * that does not exist, or that was dropped or rebased after this call, is reported by the NEXT statement
 * (zeta_exec, zeta_step, zeta_begin), never pinned here. A failed statement leaves the selection in place;
 * clear it with NULL.
 *
 * `SET zeta_branch` is refused on the embedded execute path; this is the way to select a branch.
 *
 * Returns ZETA_OK. Returns ZETA_ERR_UNKNOWN and sets `err_msg` (if non-NULL; free with zeta_free()) when
 * `db` is NULL or `name` is not valid UTF-8.
 */
int zeta_set_branch(zeta_db_t *db, const char *name, char **err_msg);

/**
 * Close a database handle and release all resources.
 * Passing NULL is a no-op.
 *
 * Also closes a session obtained from zeta_connect().
 */
void zeta_close(zeta_db_t *db);

/**
 * Cancel the statement currently running on `db`, from another thread.
 *
 * The execute entry points below (zeta_exec, zeta_step, zeta_txn_exec, ...)
 * are synchronous: they block the calling thread for the whole statement. An
 * application that wraps them — e.g. a thread pool running queries behind an
 * async server — otherwise has no way to stop a runaway query. Call this from
 * ANY OTHER thread while such a call is in flight and the running statement
 * aborts at its next cooperative checkpoint (about one scan batch) with a
 * "canceling statement due to user request" error.
 *
 * Cancellation is per-statement and self-clearing: every statement starts with
 * a fresh token, so calling this while `db` is idle does not affect later work,
 * and a cancelled handle stays fully usable — just issue the next statement.
 *
 * For a time-based bound prefer `SET statement_timeout = <ms>`, which the
 * engine enforces itself without a caller-managed timer thread.
 *
 * Returns ZETA_OK, or ZETA_ERR_UNKNOWN if `db` is NULL.
 *
 * Thread safety: this is the one call explicitly designed to run concurrently
 * with another thread's in-flight call on the same handle. The caller must
 * still not race it against zeta_close().
 */
int zeta_cancel(const zeta_db_t *db);

/* ── One-shot execution ────────────────────────────────────────────────────── */

/**
 * Execute a single SQL statement, discarding results.
 *
 * Suitable for DDL (CREATE, DROP, ALTER) and DML when the result is not
 * needed. Returns ZETA_OK or a negative error code.
 * On failure, `err_msg` (if non-NULL) is set to a newly-allocated string;
 * free with zeta_free().
 */
int zeta_exec(zeta_db_t *db, const char *sql, char **err_msg);

/**
 * Execute multiple semicolon-separated SQL statements, discarding results.
 *
 * Stops on the first error. Returns ZETA_OK or a negative error code.
 */
int zeta_exec_batch(zeta_db_t *db, const char *sql, char **err_msg);

/* ── Prepared statements ───────────────────────────────────────────────────── */

/**
 * Prepare a SQL statement for execution.
 *
 * Use $1, $2, … for parameter placeholders. Bind values with zeta_bind_*,
 * then call zeta_step() to execute / iterate rows.
 * Free with zeta_finalize() when done.
 *
 * Returns NULL on error; `err_msg` (if non-NULL) receives a newly-allocated
 * error string — free with zeta_free().
 */
zeta_stmt_t *zeta_prepare(zeta_db_t *db, const char *sql, char **err_msg);

/** Bind a 64-bit integer to parameter idx (1-based). Returns ZETA_OK or error. */
int zeta_bind_int64(zeta_stmt_t *stmt, int idx, int64_t val);

/** Bind a 32-bit integer to parameter idx (1-based). Returns ZETA_OK or error. */
int zeta_bind_int32(zeta_stmt_t *stmt, int idx, int32_t val);

/**
 * Bind a SQL boolean to parameter idx (1-based). Any nonzero `val` is true.
 *
 * Use this rather than zeta_bind_int64 for BOOLEAN columns/parameters so the
 * value is stored and read back as a boolean (true/false) rather than 1/0.
 * Returns ZETA_OK or error.
 */
int zeta_bind_bool(zeta_stmt_t *stmt, int idx, int val);

/**
 * Bind a double to parameter idx (1-based). Returns ZETA_OK or error.
 */
int zeta_bind_double(zeta_stmt_t *stmt, int idx, double val);

/**
 * Bind a UTF-8 text string to parameter idx (1-based).
 *
 * `text` is copied; caller retains ownership.
 * `len` is the byte length (not counting the null terminator);
 * pass -1 to use strlen(text).
 * Returns ZETA_OK or error.
 */
int zeta_bind_text(zeta_stmt_t *stmt, int idx, const char *text, int len);

/**
 * Bind a blob to parameter idx (1-based). `data` is copied.
 * Returns ZETA_OK or error.
 */
int zeta_bind_blob(zeta_stmt_t *stmt, int idx, const void *data, int len);

/**
 * Bind a vector (array of float32) to parameter idx (1-based).
 *
 * Use for VECTOR(n) columns and HNSW similarity queries. `floats` is copied.
 * Pass count=0 for an empty vector (floats may be NULL). Returns ZETA_OK or error.
 */
int zeta_bind_vector(zeta_stmt_t *stmt, int idx, const float *floats, int count);

/*
 * Typed temporal binds. The value reaches the plan as that type, so it works
 * as an expression operand (date_trunc('day', $1), $1 + INTERVAL '1 day',
 * SELECT $1), not only where a column type would coerce text. Payloads match
 * what zeta_column_int64() returns for ZETA_TYPE_TIMESTAMP..ZETA_TYPE_TIME.
 * Each returns ZETA_OK or error.
 */
/** SQL TIMESTAMP: µs since 1970-01-01T00:00:00. */
int zeta_bind_timestamp(zeta_stmt_t *stmt, int idx, int64_t us);
/** SQL TIMESTAMPTZ: µs since the Unix epoch, UTC. */
int zeta_bind_timestamptz(zeta_stmt_t *stmt, int idx, int64_t us);
/** SQL DATE: days since 1970-01-01. */
int zeta_bind_date(zeta_stmt_t *stmt, int idx, int32_t days);
/** SQL TIME: µs since midnight, 0 to 86400000000 (24:00:00) inclusive. */
int zeta_bind_time(zeta_stmt_t *stmt, int idx, int64_t us);

/** Bind SQL NULL to parameter idx (1-based). Returns ZETA_OK or error. */
int zeta_bind_null(zeta_stmt_t *stmt, int idx);

/**
 * Step the statement forward.
 *
 * Returns:
 *   ZETA_ROW  (1)   — a row is ready; read with zeta_column_*
 *   ZETA_DONE (100) — no more rows
 *   negative        — error
 *
 * Call repeatedly to iterate all result rows.
 */
int zeta_step(zeta_stmt_t *stmt);

/**
 * Reset a prepared statement for re-execution with new bindings.
 * Clears all bound parameters and result state.
 */
int zeta_reset(zeta_stmt_t *stmt);

/** Free a prepared statement handle. */
void zeta_finalize(zeta_stmt_t *stmt);

/* ── Column accessors (call after ZETA_ROW from zeta_step) ────────────────── */

/** Number of columns in the result set. */
int zeta_column_count(const zeta_stmt_t *stmt);

/**
 * Name of column `col` (0-based).
 *
 * Returns a pointer owned by the statement, valid until zeta_finalize() or
 * the next call to zeta_reset() (which rebuilds the column name cache).
 * Returns NULL if col is out of range.
 */
const char *zeta_column_name(const zeta_stmt_t *stmt, int col);

/**
 * Type of column `col` (0-based) for the current row — one of the
 * ZETA_TYPE_* codes: NULL, INT, FLOAT, TEXT, BLOB, BOOL, DECIMAL, UUID, JSON,
 * VECTOR.
 *
 * The code can change from row to row within a column (a NULL cell reports
 * ZETA_TYPE_NULL). Every non-NULL cell is readable with zeta_column_text().
 * Handle unknown future codes by falling back to zeta_column_text() — a
 * reader written against codes 0-5 that treats 6-9 as text keeps working.
 *
 * sqlite3_column_type() in sqlite3_compat.h is this function, so it too can
 * return codes beyond SQLite's five.
 */
int zeta_column_type(const zeta_stmt_t *stmt, int col);

/** Value of column `col` as a 64-bit integer. Returns 0 for NULL. */
int64_t zeta_column_int64(const zeta_stmt_t *stmt, int col);

/** Value of column `col` as a double. Returns 0.0 for NULL. */
double zeta_column_double(const zeta_stmt_t *stmt, int col);

/**
 * Value of column `col` as a UTF-8 string.
 *
 * The returned pointer is owned by the statement and is valid until the
 * next call to zeta_step() on this statement. Do NOT free it.
 * Returns NULL for NULL values.
 */
const char *zeta_column_text(const zeta_stmt_t *stmt, int col);

/**
 * Value of column `col` as raw bytes.
 *
 * `*len_out` is set to the number of bytes.
 * The returned pointer is valid until the next zeta_step() call.
 * Returns NULL for NULL values.
 */
const void *zeta_column_blob(const zeta_stmt_t *stmt, int col, int *len_out);

/**
 * Value of column `col` as a UTF-8 string, WITHOUT a terminating NUL.
 *
 * Zero-copy for TEXT columns: returns a pointer directly into the engine's
 * row buffer (no allocation), with `*len_out` set to the byte length. Non-text
 * values are formatted exactly as zeta_column_text() would render them. The
 * bytes are valid UTF-8 but are NOT NUL-terminated, so you MUST honor *len_out
 * (use zeta_column_text() if you need a NUL-terminated C string).
 *
 * The returned pointer is valid until the next zeta_step() / zeta_reset().
 * Returns NULL (and sets *len_out = 0) for NULL values.
 */
const char *zeta_column_str(const zeta_stmt_t *stmt, int col, int *len_out);

/* ── Transactions ──────────────────────────────────────────────────────────── */

/**
 * Begin a transaction. Returns a handle on success, NULL on error.
 * Complete with zeta_commit() or zeta_rollback().
 */
zeta_txn_t *zeta_begin(zeta_db_t *db, char **err_msg);

/** Isolation levels for zeta_begin_isolation(). */
#define ZETA_ISOLATION_SNAPSHOT     0  /* the default of zeta_begin(); PostgreSQL's REPEATABLE READ */
#define ZETA_ISOLATION_SERIALIZABLE 1  /* serializable snapshot isolation (SSI) */

/**
 * Begin a transaction at an explicit isolation level (ZETA_ISOLATION_*).
 *
 * zeta_begin() is ZETA_ISOLATION_SNAPSHOT: reads see one consistent snapshot and concurrent writes to
 * the same row conflict, but two transactions that each read what the other writes can BOTH commit
 * (write skew). With ZETA_ISOLATION_SERIALIZABLE one of them fails at zeta_commit() with a
 * serialization failure (ZETA_ERR_CONFLICT) and the caller retries.
 *
 * The level is chosen here and cannot be raised later: "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
 * inside a transaction that is not serializable is an error (it used to be accepted and ignored).
 * An unknown level is an error. Returns NULL on error and sets err_msg like zeta_begin().
 */
zeta_txn_t *zeta_begin_isolation(zeta_db_t *db, int isolation, char **err_msg);

/**
 * Execute a SQL statement within a transaction, discarding results.
 * Returns ZETA_OK or a negative error code.
 */
int zeta_txn_exec(zeta_txn_t *txn, const char *sql, char **err_msg);

/**
 * Prepare a SELECT statement within a transaction context.
 *
 * The returned statement shares transaction state with `txn`. After commit
 * or rollback, subsequent zeta_step() calls on the statement return an error.
 * Free with zeta_finalize() as usual.
 *
 * Returns NULL on error; `err_msg` (if non-NULL) receives a newly-allocated
 * error string — free with zeta_free().
 */
zeta_stmt_t *zeta_txn_prepare(zeta_txn_t *txn, const char *sql, char **err_msg);

/**
 * Commit the transaction. Frees the handle regardless of outcome.
 * Returns ZETA_OK or a negative error code.
 */
int zeta_commit(zeta_txn_t *txn, char **err_msg);

/**
 * Roll back the transaction. Frees the handle.
 * Passing NULL is a no-op. Returns ZETA_OK.
 */
int zeta_rollback(zeta_txn_t *txn);

/* ── Affected-row count ────────────────────────────────────────────────────── */

/**
 * Number of rows changed by the most recent INSERT / UPDATE / DELETE on `db`.
 *
 * Mirrors SQLite's `sqlite3_changes()`. The count is reset to 0 on every
 * `zeta_exec` and on the first `zeta_step` of a new statement. DDL (CREATE,
 * DROP, ALTER) and SELECT also reset the count to 0.
 *
 * Statements prepared with `zeta_txn_prepare` update it too, with the count
 * from inside the transaction. DML with RETURNING counts the rows it returns,
 * as PostgreSQL does.
 *
 * This is ONE counter for the whole handle. When statements run on the handle
 * from several threads, use zeta_stmt_changes() for the count of your own.
 */
int64_t zeta_changes(const zeta_db_t *db);

/**
 * Number of rows changed by `stmt`'s own most recent execution.
 *
 * Same counting rules as zeta_changes(), but the value belongs to the
 * statement: no other statement, on this handle or any other thread, can
 * overwrite it. zeta_changes() is one counter per database handle, so when
 * several statements run on one handle at once (several threads) the value you
 * read after zeta_step() can be another statement's. Use this one there
 * (compare-and-swap UPDATEs, "affected == 0" guards, DELETE loops that stop on
 * a short batch).
 *
 * It is 0 until the first zeta_step(); it is set when a step cycle starts
 * executing (the first zeta_step() after prepare or zeta_reset()), is 0 if that
 * execution failed, and stays valid until the next cycle or zeta_finalize().
 * zeta_reset() does not clear it. A statement from zeta_txn_prepare() reports
 * the count from inside the transaction. Returns 0 for a NULL `stmt`.
 */
int64_t zeta_stmt_changes(const zeta_stmt_t *stmt);

/* ── Error inspection ──────────────────────────────────────────────────────── */

/** Return the error code from the last failed operation on `db`. */
int zeta_errcode(const zeta_db_t *db);

/**
 * Return the error message from the last failed operation on `db`.
 *
 * The pointer is owned by `db` and is valid until the next call on `db`.
 * Do NOT free it.
 */
const char *zeta_errmsg(const zeta_db_t *db);

/** Return the error code from the last failed `zeta_step` call on `stmt`. */
int zeta_stmt_errcode(const zeta_stmt_t *stmt);

/**
 * Return the error message from the last failed `zeta_step` call on `stmt`.
 *
 * The pointer is owned by `stmt` and is valid until the next `zeta_step` or
 * `zeta_finalize` call. Do NOT free it.
 */
const char *zeta_stmt_errmsg(const zeta_stmt_t *stmt);

/* ── Memory management ─────────────────────────────────────────────────────── */

/**
 * Free a string returned by Zeta in an error out-parameter.
 *
 * Do NOT use this for strings returned by zeta_column_text(),
 * zeta_column_name(), or zeta_errmsg() — those are owned by their handles.
 */
void zeta_free(void *ptr);

/* ── Checkpoint / Backup ───────────────────────────────────────────────────── */

/**
 * Flush all pending writes to durable storage.
 *
 * For persistent databases this syncs the store and advances the applied-
 * watermark file. For in-memory databases this is a no-op.
 *
 * Returns ZETA_OK on success or a negative error code on failure.
 * `err_msg` (if non-NULL) receives a newly-allocated error string on failure.
 */
int zeta_checkpoint(zeta_db_t *db, char **err_msg);

/**
 * Create an online backup of the database at `backup_dir`.
 *
 * `backup_dir` must be an absolute path. For Quartz- and LSM-backed databases
 * the backup is crash-consistent (store files are copied or hard-linked, and
 * restore replays any captured log tail); open the copy with the same mode.
 * In-memory databases have no image to copy and return a negative error code
 * without creating `backup_dir`.
 *
 * Returns ZETA_OK or a negative error code.
 * `err_msg` (if non-NULL) receives a newly-allocated error string on failure.
 */
int zeta_backup(zeta_db_t *db, const char *backup_dir, char **err_msg);

/* ── Schema introspection ──────────────────────────────────────────────────── */

/**
 * Return alphabetically sorted table names as a newline-separated UTF-8 string.
 *
 * On success returns ZETA_OK and writes a heap-allocated C string to `*out`;
 * the caller must free it with zeta_free(). If the database has no tables,
 * `*out` is an empty string.
 *
 * On failure returns a negative error code and writes an error message to
 * `*err_msg` (if non-NULL); free with zeta_free().
 */
int zeta_list_tables(zeta_db_t *db, char **out, char **err_msg);

/**
 * Return the schema of `table_name` as a newline-separated UTF-8 string.
 *
 * Format (first line is the table name, followed by one line per column):
 *   <table_name>\n
 *   <col_name>\t<sql_type>\t<nullable>\t<is_primary_key>\n
 *   ...
 * where <nullable> and <is_primary_key> are '0' or '1'.
 *
 * If the table does not exist, `*out` is an empty string.
 * On success returns ZETA_OK; the caller must free `*out` with zeta_free().
 * On failure returns a negative error code.
 */
int zeta_table_info(zeta_db_t *db, const char *table_name, char **out, char **err_msg);

/* ── Embed providers ───────────────────────────────────────────────────────── */

/**
 * Register a custom callback as the SQL embed() function provider.
 *
 * `dims` — the number of float values the callback writes; must match any
 *          VECTOR(n) column or HNSW index used with embed().
 * `fn_ptr` — callback filling `buf[0..dims]` with the embedding. Return 0
 *            on success, -1 on error (and set *err_msg to a string allocated
 *            with malloc/strdup; zeta takes ownership and frees it with the
 *            C library's free(), whatever allocator zeta itself was built with).
 *
 * Replaces any previously registered provider. The provider is process-global.
 * Returns ZETA_OK or a negative error code.
 */
int zeta_set_embed_fn(
    zeta_db_t *db,
    int dims,
    int (*fn_ptr)(const char *text, float *buf, int dims, char **err_msg),
    char **err_msg
);

/**
 * Register the bundled local ONNX model as the embed() provider.
 *
 * `model_dir` — directory containing `model.onnx` and `vocab.txt`.
 * On success writes the output dimension (e.g. 384) to `*dims_out`.
 *
 * Requires the engine to be built with the `local-embed` feature; otherwise
 * returns a negative error code.
 */
int zeta_use_local_embed(
    zeta_db_t *db,
    const char *model_dir,
    int *dims_out,
    char **err_msg
);

/**
 * Register the OpenAI Embeddings API as the embed() provider.
 *
 * `api_key` — OpenAI secret key ("sk-...").
 * `model`   — "text-embedding-3-small", "text-embedding-3-large", or
 *             "text-embedding-ada-002".
 *
 * Makes a validation request immediately; returns a negative error code if
 * the key is invalid. Requires the engine to be built with the `openai-embed`
 * feature.
 */
int zeta_use_openai_embed(
    zeta_db_t *db,
    const char *api_key,
    const char *model,
    char **err_msg
);

/**
 * Register Voyage AI as the embed() provider.
 *
 * `api_key` — Voyage AI key.
 * `model`   — "voyage-3", "voyage-3-lite", "voyage-code-3", or "voyage-finance-2".
 *
 * Makes a validation request immediately; returns a negative error code if
 * the key is invalid. Requires the engine to be built with the `voyage-embed`
 * feature.
 */
int zeta_use_voyage_embed(
    zeta_db_t *db,
    const char *api_key,
    const char *model,
    char **err_msg
);

/* ── Embedded wire-protocol listeners ─────────────────────────────────────── */

/*
 * Optional: gated on the `wire-pg` (alias `dev-listeners`) Cargo feature in
 * zeta-embedded-c (the shipped archives have it), and on the ZETA_LISTENERS
 * macro in this header. ZETA_DEV_LISTENERS, the name this macro had while the
 * listeners were a developer facility, is still accepted. Define one of them
 * before including zeta.h to expose the declarations; they are linkable only
 * against a libzeta built with the feature.
 *
 * A listener opens a TCP socket that standard PostgreSQL clients (psql,
 * drivers, GUI tools) can connect to, serving the in-process database. The
 * embedded library has no MySQL listener; archives up to v0.1.12 had one
 * (zeta_start_mysqlwire*), and it was removed for the first public release.
 *
 * A listener is exactly as safe as the way it is started:
 *
 *   zeta_start_pgwire
 *       Trust authentication: ANYONE WHO CAN REACH THE PORT has full
 *       read/write/DDL access. A non-loopback address is refused unless
 *       ZETA_ALLOW_NONLOCAL_EMBED_LISTEN=1 is set in the environment, and then
 *       logs a WARN. For local development and local tools.
 *
 *   zeta_start_pgwire_auth
 *       A non-NULL password is required of every client (SCRAM-SHA-256, user
 *       "zeta") and permits a non-loopback address without the environment
 *       opt-out (logged at WARN when there is no TLS, INFO with it). There is
 *       ONE account with full access: no per-user roles, no per-database
 *       grants. A NULL password is trust authentication, as above.
 *
 *   zeta_start_pgwire_tls
 *       As _auth, plus TLS (rustls) from a PEM certificate chain and private
 *       key. When a certificate is given TLS is required of every client; it
 *       is loaded before the bind, so a bad path fails the call. Use it
 *       whenever a network can see the port: without TLS, queries and results
 *       travel in cleartext (SCRAM protects the password, not the traffic).
 *
 * Limits: 256 concurrent connections per listener. Statements are cut off
 * after 5 minutes by default (a logged-in client can raise that with
 * SET statement_timeout). Replication slots are kept in memory only.
 *
 * "Full access" includes the host: SQL can call the engine's file-reading
 * functions (for example read_parquet('<path>'), which opens any path the host
 * process can read, with no allow-list).
 *
 * Before login. A connection has a login window (`login_timeout_secs` on the
 * Rust config structs, 60 s by default, PostgreSQL's own default; the C entry
 * points use the default) to finish logging in -- TLS negotiation, startup,
 * authentication -- and is closed when it does not; a logged-in connection
 * (one in COPY, or waiting for a Sync, included) is never timed by it. What a
 * password does NOT protect: unauthenticated sockets count against the 256 for
 * that window, so a peer that can reach the port can keep every slot busy by
 * reconnecting; failed logins are not throttled; each attempt that names user
 * "zeta" costs the host a SCRAM key derivation (about 50 ms of CPU) before any
 * proof is checked; and a connection may hold up to the cap per message and per
 * statement, not a total (PostgreSQL's own message-size cap applies). Archives up
 * to v0.1.12 predate all of this: there an idle socket holds its slot for ever,
 * and their PostgreSQL listener lets a client skip the password (fixed in engine
 * #4841), so never expose one. Their MySQL listener, removed since, also read freed
 * memory on pipelined commands and panicked on a bad sequence id.
 * Expose a listener only to networks and peers you trust: a private network, a
 * VPN or a firewall allow-list. It is not for the open internet.
 *
 * A listener runs inside the host process, so a client's queries use the
 * application's memory and CPU, and a fault in the wire layer is the host's
 * fault. For a database that is its own process, run zeta-server-bin.
 */

#if defined(ZETA_LISTENERS) || defined(ZETA_DEV_LISTENERS)

/**
 * Start a PostgreSQL wire-protocol listener serving `db`, with trust
 * authentication: anyone who can reach the port gets full access to the
 * database (see the section note above). `addr` is a TCP bind string
 * ("127.0.0.1:5433" by convention); port 0 lets the OS choose. The listener
 * spawns onto the embedded database's Tokio runtime.
 *
 * Returns ZETA_OK on success or a negative error code on failure.
 * `err_msg` (if non-NULL) receives a newly-allocated error string on
 * failure — free with zeta_free().
 *
 * Requires libzeta built with the `wire-pg` (or `dev-listeners`) feature.
 */
int zeta_start_pgwire(zeta_db_t *db, const char *addr, char **err_msg);

/**
 * Start a PostgreSQL listener with optional authentication.
 *
 * `password` non-NULL: SCRAM-SHA-256 for user "zeta", and `addr` may be a
 * non-loopback address. NULL: trust authentication, exactly as
 * zeta_start_pgwire. Never pass an empty string: it is not a password. The engine
 * sources now refuse it; the archives up to v0.1.12 accept it and treat the
 * listener as authenticated, so a non-loopback address then binds without a
 * real check (PostgreSQL still runs SCRAM, but against an empty secret, which
 * libpq refuses to send). Other arguments and the return value as zeta_start_pgwire.
 */
int zeta_start_pgwire_auth(
    zeta_db_t *db,
    const char *addr,
    const char *password,
    char **err_msg
);

/**
 * Start a PostgreSQL listener with optional authentication and optional TLS.
 *
 * `tls_cert` and `tls_key` are paths to a PEM certificate chain and private
 * key. Both non-NULL: the listener requires TLS (a plaintext client is
 * rejected). Both NULL: plaintext, as zeta_start_pgwire_auth.
 * Exactly one is an error. Other arguments as zeta_start_pgwire_auth.
 */
int zeta_start_pgwire_tls(
    zeta_db_t *db,
    const char *addr,
    const char *password,
    const char *tls_cert,
    const char *tls_key,
    char **err_msg
);

/**
 * Stop and drain all wire-protocol listeners associated with `db`.
 * `zeta_close` invokes this internally; explicit use is for callers
 * that want to take down listeners without closing the database.
 *
 * Returns ZETA_OK or a negative error code.  The declaration is
 * visible in this header only when ZETA_LISTENERS (or ZETA_DEV_LISTENERS)
 * is defined, the same gate as the start functions; the symbol is exported by
 * libzeta whenever the `wire-pg` feature (alias `dev-listeners`) is compiled in.
 */
int zeta_stop_listeners(zeta_db_t *db);

#endif /* ZETA_LISTENERS || ZETA_DEV_LISTENERS */

/* ── OLAP routing observability ────────────────────────────────────────────── */

/*
 * Optional: gated on the `olap` Cargo feature in zeta-embedded-c, and on the
 * matching ZETA_OLAP macro in this header. Define ZETA_OLAP before including
 * zeta.h to expose the declaration; the symbol is linkable only against a
 * libzeta built with the `olap` feature. The database must have been opened
 * with an OLAP-capable backend (zeta_open_mode with ZETA_MODE_LSM / QUARTZ /
 * MEMORY) for an engine to be present.
 */
#ifdef ZETA_OLAP
/**
 * Report analytical-routing counters for the in-process columnar engine.
 *
 * Each out-param is optional (pass NULL to skip). On success returns ZETA_OK
 * and fills the non-NULL out-params:
 *   - queries_routed        — analytical queries served by the columnar engine
 *   - declined_stale        — declines: columnar tier not yet fresh enough
 *   - declined_unsupported  — declines: query shape not supported by OLAP
 *   - declined_error        — declines: an execution error forced row fallback
 *   - applied_lsn / applied_ts — the applier's watermark (LSN + timestamp)
 *
 * Returns ZETA_OLAP_ABSENT if no OLAP engine is running (out-params untouched),
 * distinct from ZETA_OK with zero counters (engine present, nothing routed yet).
 * On other failures returns a negative error code and, if `err_msg` is non-NULL,
 * sets it to a newly-allocated string — free with zeta_free().
 */
int zeta_olap_stats(zeta_db_t *db,
                    uint64_t *queries_routed,
                    uint64_t *declined_stale,
                    uint64_t *declined_unsupported,
                    uint64_t *declined_error,
                    uint64_t *applied_lsn,
                    uint64_t *applied_ts,
                    char **err_msg);
#endif /* ZETA_OLAP */

/* ── Miscellaneous ─────────────────────────────────────────────────────────── */

/**
 * Return the Zeta version string (e.g. "0.1.0").
 * The returned pointer is static and never needs to be freed.
 */
const char *zeta_version(void);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* ZETA_H */
