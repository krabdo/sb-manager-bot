package manager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type Store struct{ db *sql.DB }

func OpenStore(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure database: %w", err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		db.Close()
		return nil, fmt.Errorf("database integrity check failed: %w", err)
	}
	if check != "ok" {
		db.Close()
		return nil, fmt.Errorf("database integrity check failed: %s", check)
	}
	_ = os.Chmod(path, 0600)
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS accounts(
 telegram_user_id INTEGER PRIMARY KEY, chat_id INTEGER NOT NULL, forum_user_id TEXT NOT NULL,
 cookie_cipher BLOB NOT NULL, status TEXT NOT NULL CHECK(status IN ('active','paused','rebind_required')),
 next_poll_at INTEGER NOT NULL, failure_count INTEGER NOT NULL DEFAULT 0, last_success_at INTEGER,
 last_error_code TEXT NOT NULL DEFAULT '', auth_alerted INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS accounts_due ON accounts(status,next_poll_at,telegram_user_id);
CREATE TABLE IF NOT EXISTS seen_notifications(
 telegram_user_id INTEGER NOT NULL REFERENCES accounts(telegram_user_id) ON DELETE CASCADE,
 notification_id TEXT NOT NULL, seen_at INTEGER NOT NULL, PRIMARY KEY(telegram_user_id,notification_id));
CREATE INDEX IF NOT EXISTS seen_recent ON seen_notifications(telegram_user_id,seen_at DESC);
CREATE TABLE IF NOT EXISTS banned_users(telegram_user_id INTEGER PRIMARY KEY, reason TEXT NOT NULL DEFAULT '', banned_at INTEGER NOT NULL, admin_id INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS runtime_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS bot_state(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT OR IGNORE INTO runtime_settings(key,value) VALUES('global_pause','false');
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(1,unixepoch());`
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

func (s *Store) ValidateCookies(ctx context.Context, cipher *CookieCipher) error {
	rows, err := s.db.QueryContext(ctx, `SELECT telegram_user_id,forum_user_id,cookie_cipher FROM accounts`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tid int64
		var fid string
		var blob []byte
		if err := rows.Scan(&tid, &fid, &blob); err != nil {
			return err
		}
		if _, err := cipher.Decrypt(blob, tid, fid); err != nil {
			return fmt.Errorf("stored credential validation failed for Telegram ID %d: %w", tid, err)
		}
	}
	return rows.Err()
}

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var next int64
	var last sql.NullInt64
	var alerted int
	err := row.Scan(&a.TelegramID, &a.ChatID, &a.ForumUserID, &a.CookieCipher, &a.Status, &next, &a.FailureCount, &last, &a.LastErrorCode, &alerted)
	if err != nil {
		return a, err
	}
	a.NextPollAt = time.Unix(next, 0)
	a.AuthAlerted = alerted != 0
	if last.Valid {
		t := time.Unix(last.Int64, 0)
		a.LastSuccessAt = &t
	}
	return a, nil
}

func (s *Store) Account(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT telegram_user_id,chat_id,forum_user_id,cookie_cipher,status,next_poll_at,failure_count,last_success_at,last_error_code,auth_alerted FROM accounts WHERE telegram_user_id=?`, id))
}

func (s *Store) Bind(ctx context.Context, a Account, maxUsers int, baseline []string) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var oldForum string
	e := tx.QueryRowContext(ctx, `SELECT forum_user_id FROM accounts WHERE telegram_user_id=?`, a.TelegramID).Scan(&oldForum)
	if errors.Is(e, sql.ErrNoRows) {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
			return false, err
		}
		if n >= maxUsers {
			return false, ErrCapacity
		}
		changed = true
	} else if e != nil {
		return false, e
	} else {
		changed = oldForum != a.ForumUserID
	}
	if changed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM seen_notifications WHERE telegram_user_id=?`, a.TelegramID); err != nil {
			return false, err
		}
	}
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(telegram_user_id,chat_id,forum_user_id,cookie_cipher,status,next_poll_at,failure_count,last_error_code,auth_alerted,created_at,updated_at) VALUES(?,?,?,?,?,?,0,'',0,?,?) ON CONFLICT(telegram_user_id) DO UPDATE SET chat_id=excluded.chat_id,forum_user_id=excluded.forum_user_id,cookie_cipher=excluded.cookie_cipher,status='active',next_poll_at=excluded.next_poll_at,failure_count=0,last_error_code='',auth_alerted=0,updated_at=excluded.updated_at`, a.TelegramID, a.ChatID, a.ForumUserID, a.CookieCipher, StatusActive, a.NextPollAt.Unix(), now, now)
	if err != nil {
		return false, err
	}
	if changed {
		for _, notificationID := range baseline {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO seen_notifications(telegram_user_id,notification_id,seen_at) VALUES(?,?,?)`, a.TelegramID, notificationID, time.Now().UnixNano()); err != nil {
				return false, err
			}
		}
	}
	return changed, tx.Commit()
}

func (s *Store) IsBanned(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM banned_users WHERE telegram_user_id=?`, id).Scan(&n)
	return n > 0, err
}
func (s *Store) Ban(ctx context.Context, id, admin int64) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM accounts WHERE telegram_user_id=?`, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO banned_users(telegram_user_id,banned_at,admin_id) VALUES(?,?,?) ON CONFLICT(telegram_user_id) DO UPDATE SET banned_at=excluded.banned_at,admin_id=excluded.admin_id`, id, time.Now().Unix(), admin); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Unban(ctx context.Context, id int64) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM banned_users WHERE telegram_user_id=?`, id)
	return e
}
func (s *Store) Unbind(ctx context.Context, id int64) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE telegram_user_id=?`, id)
	return e
}
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	r, e := s.db.ExecContext(ctx, `UPDATE accounts SET status=?,updated_at=? WHERE telegram_user_id=?`, status, time.Now().Unix(), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AddSeen(ctx context.Context, id int64, notificationID string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO seen_notifications(telegram_user_id,notification_id,seen_at) VALUES(?,?,?)`, id, notificationID, time.Now().UnixNano()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM seen_notifications WHERE telegram_user_id=? AND notification_id NOT IN (SELECT notification_id FROM seen_notifications WHERE telegram_user_id=? ORDER BY seen_at DESC LIMIT 2048)`, id, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) IsSeen(ctx context.Context, id int64, nid string) (bool, error) {
	var n int
	e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM seen_notifications WHERE telegram_user_id=? AND notification_id=?`, id, nid).Scan(&n)
	return n > 0, e
}
func (s *Store) SeenCount(ctx context.Context, id int64) (int, error) {
	var n int
	e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM seen_notifications WHERE telegram_user_id=?`, id).Scan(&n)
	return n, e
}

func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]Account, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT telegram_user_id,chat_id,forum_user_id,cookie_cipher,status,next_poll_at,failure_count,last_success_at,last_error_code,auth_alerted FROM accounts WHERE status='active' AND next_poll_at<=? ORDER BY next_poll_at,telegram_user_id LIMIT ?`, now.Unix(), limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, e := scanAccount(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Schedule(ctx context.Context, id int64, next time.Time, failures int, lastSuccess *time.Time, code string) error {
	var last any
	if lastSuccess != nil {
		last = lastSuccess.Unix()
	}
	_, e := s.db.ExecContext(ctx, `UPDATE accounts SET next_poll_at=?,failure_count=?,last_success_at=COALESCE(?,last_success_at),last_error_code=?,updated_at=? WHERE telegram_user_id=?`, next.Unix(), failures, last, code, time.Now().Unix(), id)
	return e
}
func (s *Store) MarkRebind(ctx context.Context, id int64) (bool, error) {
	a, e := s.Account(ctx, id)
	if e != nil {
		return false, e
	}
	first := !a.AuthAlerted
	_, e = s.db.ExecContext(ctx, `UPDATE accounts SET status='rebind_required',auth_alerted=1,last_error_code='authentication',updated_at=? WHERE telegram_user_id=?`, time.Now().Unix(), id)
	return first, e
}

func (s *Store) GlobalPaused(ctx context.Context) (bool, error) {
	var v string
	e := s.db.QueryRowContext(ctx, `SELECT value FROM runtime_settings WHERE key='global_pause'`).Scan(&v)
	return v == "true", e
}
func (s *Store) SetGlobalPaused(ctx context.Context, p bool) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO runtime_settings(key,value) VALUES('global_pause',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatBool(p))
	return e
}

func (s *Store) ActivateGlobalPause(ctx context.Context) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE runtime_settings SET value='true' WHERE key='global_pause' AND value!='true'`)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}
func (s *Store) UpdateOffset(ctx context.Context, n int64) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO bot_state(key,value) VALUES('update_offset',?) ON CONFLICT(key) DO UPDATE SET value=CASE WHEN CAST(excluded.value AS INTEGER)>CAST(bot_state.value AS INTEGER) THEN excluded.value ELSE bot_state.value END`, strconv.FormatInt(n, 10))
	return e
}
func (s *Store) UpdateOffsetValue(ctx context.Context) (int64, error) {
	var v string
	e := s.db.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key='update_offset'`).Scan(&v)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	return strconv.ParseInt(v, 10, 64)
}
func (s *Store) Writable(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO runtime_settings(key,value) VALUES('ready_probe',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(time.Now().UnixNano(), 10))
	return e
}
func (s *Store) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var st Stats
	e := s.db.QueryRowContext(ctx, `SELECT coalesce(sum(status='active'),0),coalesce(sum(status='paused'),0),coalesce(sum(status='rebind_required'),0),coalesce(sum(status='active' AND next_poll_at<=?),0) FROM accounts`, now.Unix()).Scan(&st.Active, &st.Paused, &st.RebindRequired, &st.Backlog)
	if e != nil {
		return st, e
	}
	e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM banned_users`).Scan(&st.Banned)
	return st, e
}
