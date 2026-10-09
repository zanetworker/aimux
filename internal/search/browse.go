package search

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Detail is what a picker preview shows for one session.
type Detail struct {
	SessionID     string
	Path          string
	CWD           string
	Title         string
	FirstPrompt   string
	Automated     bool
	ModTime       time.Time
	Exchanges     int
	RecentPrompts []string // oldest first
}

// Recent lists sessions by last activity, newest first.
func (ix *Index) Recent(opts SearchOpts) ([]Result, error) {
	if opts.Limit <= 0 {
		opts.Limit = 200
	}
	rows, err := ix.db.Query(`
		SELECT id, path, cwd, title, automated, mtime FROM sessions
		WHERE automated = 0 OR ?
		ORDER BY mtime DESC LIMIT ?`, boolInt(opts.IncludeAutomated), opts.Limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Result
	for rows.Next() {
		var r Result
		var auto int
		var mtime int64
		if err := rows.Scan(&r.SessionID, &r.Path, &r.CWD, &r.Title, &auto, &mtime); err != nil {
			return nil, err
		}
		r.Automated = auto == 1
		r.ModTime = time.Unix(0, mtime)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Detail returns preview data for one session, with its last n prompts.
func (ix *Index) Detail(sessionID string, n int) (Detail, error) {
	d := Detail{SessionID: sessionID}
	var auto int
	var mtime int64
	err := ix.db.QueryRow(`SELECT path, cwd, title, first_prompt, automated, mtime FROM sessions WHERE id = ?`, sessionID).
		Scan(&d.Path, &d.CWD, &d.Title, &d.FirstPrompt, &auto, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return d, fmt.Errorf("session %s is not in the index", sessionID)
	}
	if err != nil {
		return d, err
	}
	d.Automated = auto == 1
	d.ModTime = time.Unix(0, mtime)
	if err := ix.db.QueryRow(`SELECT count(*) FROM chunks WHERE session_id = ?`, sessionID).Scan(&d.Exchanges); err != nil {
		return d, err
	}
	rows, err := ix.db.Query(`SELECT prompt FROM chunks WHERE session_id = ? ORDER BY CAST(seq AS INTEGER) DESC LIMIT ?`, sessionID, n)
	if err != nil {
		return d, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return d, err
		}
		d.RecentPrompts = append([]string{p}, d.RecentPrompts...)
	}
	return d, rows.Err()
}
