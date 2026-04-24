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
/** Unknown / unclassified error. */
#define ZETA_ERR_UNKNOWN     (-99)

/* ── Column type codes ─────────────────────────────────────────────────────── */

#define ZETA_TYPE_NULL   0
#define ZETA_TYPE_INT    1
#define ZETA_TYPE_FLOAT  2
#define ZETA_TYPE_TEXT   3
#define ZETA_TYPE_BLOB   4
/** SQL BOOLEAN. Read with zeta_column_int64() — returns 0 (false) or 1 (true). */
#define ZETA_TYPE_BOOL   5

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
 */
zeta_db_t *zeta_open(const char *path, char **err_msg);

/**
 * Open a pure in-memory database. Data is lost when the handle is closed.
 */
zeta_db_t *zeta_open_memory(char **err_msg);

/**
 * Close a database handle and release all resources.
 * Passing NULL is a no-op.
 */
void zeta_close(zeta_db_t *db);

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
 * Type of column `col` (0-based) for the current row.
 * One of: ZETA_TYPE_NULL, ZETA_TYPE_INT, ZETA_TYPE_FLOAT,
 *         ZETA_TYPE_TEXT, ZETA_TYPE_BLOB.
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

/* ── Transactions ──────────────────────────────────────────────────────────── */

/**
 * Begin a transaction. Returns a handle on success, NULL on error.
 * Complete with zeta_commit() or zeta_rollback().
 */
zeta_txn_t *zeta_begin(zeta_db_t *db, char **err_msg);

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
 */
int64_t zeta_changes(const zeta_db_t *db);

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
 * `backup_dir` must be an absolute path. For LSM-backed databases the backup
 * is crash-consistent (SST files are hard-linked and restore will replay any
 * captured log tail). BTree-backed persistent databases (the default for
 * zeta_open) do not currently produce a complete restorable MVCC snapshot
 * and will return a negative error code — use an LSM-backed database when
 * crash-consistent backups are required.
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
 *            with a compatible allocator; zeta takes ownership).
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
