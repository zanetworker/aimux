package session

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Compile-time check.
var _ Store = (*SQLiteStore)(nil)

// SQLiteStore implements Store backed by a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  agent_config TEXT,
  environment TEXT NOT NULL,
  sandbox_name TEXT,
  provider TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'created',
  model TEXT,
  working_dir TEXT,
  harness TEXT NOT NULL DEFAULT 'native',
  start_time TEXT NOT NULL,
  last_activity TEXT,
  tokens_in INTEGER DEFAULT 0,
  tokens_out INTEGER DEFAULT 0,
  cost_usd REAL DEFAULT 0,
  error TEXT,
  metadata TEXT
);

CREATE TABLE IF NOT EXISTS session_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  timestamp TEXT NOT NULL,
  type TEXT NOT NULL,
  data TEXT,
  FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_environment ON sessions(environment);
CREATE INDEX IF NOT EXISTS idx_session_events_session ON session_events(session_id);
`

// NewSQLiteStore opens (or creates) a SQLite database at the given path
// and initializes the schema.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite store: enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite store: enable foreign keys: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite store: set busy timeout: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite store: create schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection for migration use.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

func (s *SQLiteStore) Create(sess *Session) error {
	harness := sess.Harness
	if harness == "" {
		harness = "native"
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, agent_config, environment, sandbox_name, provider, status, model, working_dir, harness, start_time, last_activity, tokens_in, tokens_out, cost_usd, error, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.AgentConfig, sess.Environment, sess.SandboxName,
		sess.Provider, string(sess.Status), sess.Model, sess.WorkingDir,
		harness, sess.StartTime.Format(time.RFC3339Nano),
		sess.LastActivity.Format(time.RFC3339Nano),
		sess.TokensIn, sess.TokensOut, sess.CostUSD,
		sess.Error, sess.Metadata,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("session %q already exists", sess.ID)
		}
		return fmt.Errorf("sqlite store: create session %q: %w", sess.ID, err)
	}
	return nil
}

func (s *SQLiteStore) Get(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, agent_config, environment, sandbox_name, provider, status, model, working_dir, harness, start_time, last_activity, tokens_in, tokens_out, cost_usd, error, metadata
		 FROM sessions WHERE id = ?`, id,
	)
	sess, err := scanSession(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session %q not found", id)
		}
		return nil, fmt.Errorf("sqlite store: get session %q: %w", id, err)
	}
	return sess, nil
}

func (s *SQLiteStore) List() ([]*Session, error) {
	rows, err := s.db.Query(
		`SELECT id, agent_config, environment, sandbox_name, provider, status, model, working_dir, harness, start_time, last_activity, tokens_in, tokens_out, cost_usd, error, metadata
		 FROM sessions ORDER BY start_time DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list sessions: %w", err)
	}
	defer rows.Close()

	var result []*Session
	for rows.Next() {
		sess, err := scanSessionRows(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite store: scan session: %w", err)
		}
		result = append(result, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite store: iterate sessions: %w", err)
	}
	if result == nil {
		result = make([]*Session, 0)
	}
	return result, nil
}

func (s *SQLiteStore) Update(sess *Session) error {
	harness := sess.Harness
	if harness == "" {
		harness = "native"
	}
	res, err := s.db.Exec(
		`UPDATE sessions SET agent_config=?, environment=?, sandbox_name=?, provider=?, status=?, model=?, working_dir=?, harness=?, start_time=?, last_activity=?, tokens_in=?, tokens_out=?, cost_usd=?, error=?, metadata=?
		 WHERE id=?`,
		sess.AgentConfig, sess.Environment, sess.SandboxName,
		sess.Provider, string(sess.Status), sess.Model, sess.WorkingDir,
		harness, sess.StartTime.Format(time.RFC3339Nano),
		sess.LastActivity.Format(time.RFC3339Nano),
		sess.TokensIn, sess.TokensOut, sess.CostUSD,
		sess.Error, sess.Metadata,
		sess.ID,
	)
	if err != nil {
		return fmt.Errorf("sqlite store: update session %q: %w", sess.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite store: update session %q: %w", sess.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("session %q not found", sess.ID)
	}
	return nil
}

func (s *SQLiteStore) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite store: delete session %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite store: delete session %q: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("session %q not found", id)
	}
	return nil
}

func (s *SQLiteStore) AddEvent(sessionID string, evt *SessionEvent) error {
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("sqlite store: add event: session %q not found", sessionID)
		}
		return fmt.Errorf("sqlite store: add event: check session %q: %w", sessionID, err)
	}

	_, err := s.db.Exec(
		`INSERT INTO session_events (session_id, timestamp, type, data)
		 VALUES (?, ?, ?, ?)`,
		sessionID, evt.Timestamp.Format(time.RFC3339Nano), evt.Type, evt.Data,
	)
	if err != nil {
		return fmt.Errorf("sqlite store: add event to session %q: %w", sessionID, err)
	}
	return nil
}

func (s *SQLiteStore) ListEvents(sessionID string) ([]SessionEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, timestamp, type, data
		 FROM session_events WHERE session_id = ? ORDER BY id ASC`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list events for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	var result []SessionEvent
	for rows.Next() {
		var evt SessionEvent
		var ts string
		var data sql.NullString
		if err := rows.Scan(&evt.ID, &evt.SessionID, &ts, &evt.Type, &data); err != nil {
			return nil, fmt.Errorf("sqlite store: scan event: %w", err)
		}
		evt.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		if data.Valid {
			evt.Data = data.String
		}
		result = append(result, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite store: iterate events: %w", err)
	}
	if result == nil {
		result = make([]SessionEvent, 0)
	}
	return result, nil
}

// scanner interface shared by *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanFromRow(s scanner) (*Session, error) {
	var sess Session
	var status string
	var startTime, lastActivity string
	var agentConfig, sandboxName, model, workingDir, harness, errStr, metadata sql.NullString

	err := s.Scan(
		&sess.ID, &agentConfig, &sess.Environment, &sandboxName,
		&sess.Provider, &status, &model, &workingDir,
		&harness, &startTime, &lastActivity,
		&sess.TokensIn, &sess.TokensOut, &sess.CostUSD,
		&errStr, &metadata,
	)
	if err != nil {
		return nil, err
	}

	sess.Status = SessionStatus(status)
	sess.StartTime, _ = time.Parse(time.RFC3339Nano, startTime)
	sess.LastActivity, _ = time.Parse(time.RFC3339Nano, lastActivity)
	if agentConfig.Valid {
		sess.AgentConfig = agentConfig.String
	}
	if sandboxName.Valid {
		sess.SandboxName = sandboxName.String
	}
	if model.Valid {
		sess.Model = model.String
	}
	if workingDir.Valid {
		sess.WorkingDir = workingDir.String
	}
	if harness.Valid {
		sess.Harness = harness.String
	}
	if errStr.Valid {
		sess.Error = errStr.String
	}
	if metadata.Valid {
		sess.Metadata = metadata.String
	}
	return &sess, nil
}

func scanSession(row *sql.Row) (*Session, error) {
	return scanFromRow(row)
}

func scanSessionRows(rows *sql.Rows) (*Session, error) {
	return scanFromRow(rows)
}

func isUniqueViolation(err error) bool {
	return err != nil && contains(err.Error(), "UNIQUE constraint failed")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
