package wisp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // SQLite driver registration is infrastructure, not a capability.
)

// ErrNotFound means no run has the requested ID.
var ErrNotFound = errors.New("wisp: run not found")

// RunRecord is an execution summary. Final output is observable here; it is
// never automatically delivered to the event's originating transport.
type RunRecord struct {
	ID         string     `json:"id"`
	EventID    string     `json:"event_id"`
	Source     string     `json:"source"`
	Event      Event      `json:"event"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Activity records a model request/response, tool call/result, or run transition.
// Sequence is durable and globally increasing, including across restarts.
type Activity struct {
	Sequence int64           `json:"sequence"`
	RunID    string          `json:"run_id"`
	Kind     string          `json:"kind"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}

type History struct {
	Run      RunRecord  `json:"run"`
	Activity []Activity `json:"activity"`
}

type store struct {
	db *sql.DB
	// An in-memory database needs a live connection even when database/sql
	// replaces an operational connection after a canceled transaction.
	keeper *sql.Conn
}

func (s *store) close() error {
	var err error
	if s.keeper != nil {
		err = s.keeper.Close()
	}
	return errors.Join(err, s.db.Close())
}

func openStore(path string) (_ *store, err error) {
	params := url.Values{"_pragma": {"foreign_keys(1)", "busy_timeout(5000)"}}
	var uri url.URL
	if path == ":memory:" {
		params.Set("mode", "memory")
		params.Set("cache", "shared")
		uri = url.URL{Scheme: "file", Opaque: "wisp-" + rand.Text(), RawQuery: params.Encode()}
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		uri = url.URL{Scheme: "file", Path: absolute, RawQuery: params.Encode()}
	}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	defer func() {
		if err != nil {
			_ = s.close()
		}
	}()
	if path == ":memory:" {
		db.SetMaxOpenConns(2)
		if s.keeper, err = db.Conn(context.Background()); err != nil {
			return nil, err
		}
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, err
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version > 1 {
		return nil, fmt.Errorf("wisp: unsupported database version %d", version)
	}
	if _, err = db.Exec(`
	BEGIN;
	CREATE TABLE IF NOT EXISTS events (
	 id TEXT PRIMARY KEY, source TEXT NOT NULL, payload BLOB NOT NULL
	);
	CREATE TABLE IF NOT EXISTS runs (
	 id TEXT PRIMARY KEY, event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
	 status TEXT NOT NULL CHECK(status IN ('running','completed','failed')),
	 started_at TEXT NOT NULL, finished_at TEXT, output TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS activity (
	 sequence INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES runs(id),
	 kind TEXT NOT NULL, time TEXT NOT NULL, data BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS activity_run ON activity(run_id, sequence);
	PRAGMA user_version=1;
	COMMIT;`); err != nil {
		return nil, err
	}
	// A restart identifies abandoned work; it never replays external effects.
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := timestamp(time.Now())
	if _, err = tx.Exec(`INSERT INTO activity(run_id,kind,time,data)
	 SELECT id,'run.failed',?,? FROM runs WHERE status='running'`, now, `{"error":"runtime interrupted before completion"}`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE runs SET status='failed',finished_at=?,error='runtime interrupted before completion' WHERE status='running'`, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *store) accept(ctx context.Context, runID, eventID, source string, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(id,source,payload) VALUES(?,?,?)`, eventID, source, payload); err != nil {
		return err
	}
	now := timestamp(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,event_id,status,started_at) VALUES(?,?,'running',?)`, runID, eventID, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO activity(run_id,kind,time,data) VALUES(?,'run.started',?,?)`, runID, now, `{"event_id":"`+eventID+`"}`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) record(ctx context.Context, id, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO activity(run_id,kind,time,data) VALUES(?,?,?,?)`, id, kind, timestamp(time.Now()), b)
	return err
}

func (s *store) finish(id, output string, runErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, message := "completed", ""
	if runErr != nil {
		status, message = "failed", runErr.Error()
	}
	data, err := json.Marshal(map[string]string{"output": output, "error": message})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := timestamp(time.Now())
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,finished_at=?,output=?,error=? WHERE id=?`, status, now, output, message, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO activity(run_id,kind,time,data) VALUES(?,?,?,?)`, id, "run."+status, now, data); err != nil {
		return err
	}
	return tx.Commit()
}

const runQuery = `SELECT r.id,r.event_id,e.source,e.payload,r.status,r.started_at,r.finished_at,r.output,r.error FROM runs r JOIN events e ON e.id=r.event_id`

type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (RunRecord, error) {
	var r RunRecord
	var event []byte
	var started string
	var finished sql.NullString
	err := row.Scan(&r.ID, &r.EventID, &r.Source, &event, &r.Status, &started, &finished, &r.Output, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(event, &r.Event); err != nil {
		return r, err
	}
	if r.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return r, err
	}
	if finished.Valid {
		t, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return r, err
		}
		r.FinishedAt = &t
	}
	return r, nil
}

// ListRuns returns up to limit runs, newest first. Limits must be between 1 and
// 1000. History is for inspection only; the run engine never reads it as context.
func (r *Runtime) ListRuns(ctx context.Context, limit int) ([]RunRecord, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("wisp: limit must be between 1 and 1000")
	}
	rows, err := r.store.db.QueryContext(ctx, runQuery+` ORDER BY r.rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunRecord{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

// History returns a consistent snapshot of a run and its persisted timeline.
func (r *Runtime) History(ctx context.Context, id string) (History, error) {
	var h History
	tx, err := r.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return h, err
	}
	defer tx.Rollback()
	if h.Run, err = scanRun(tx.QueryRowContext(ctx, runQuery+` WHERE r.id=?`, id)); err != nil {
		return h, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,run_id,kind,time,data FROM activity WHERE run_id=? ORDER BY sequence`, id)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	h.Activity = []Activity{}
	for rows.Next() {
		var a Activity
		var at string
		var data []byte
		if err = rows.Scan(&a.Sequence, &a.RunID, &a.Kind, &at, &data); err != nil {
			return h, err
		}
		a.Data = data
		if a.Time, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return h, err
		}
		h.Activity = append(h.Activity, a)
	}
	if err = rows.Err(); err != nil {
		return h, err
	}
	return h, tx.Commit()
}
