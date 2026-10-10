package search

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	dirSQL, dirArgs := scopeClause(opts)
	// #nosec G202 -- dirSQL is a fixed clause; values are bound
	rows, err := ix.db.Query(`
		SELECT s.id, s.path, s.cwd, s.title, s.automated, s.mtime FROM sessions s
		WHERE (s.automated = 0 OR ?)`+dirSQL+`
		ORDER BY s.mtime DESC LIMIT ?`, append(append([]any{boolInt(opts.IncludeAutomated)}, dirArgs...), opts.Limit)...)
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

// Exchange is one indexed exchange: the prompt that opened it and what was
// said (human and assistant text, no tool output).
type Exchange struct {
	Seq    int
	Prompt string
	Prose  string
}

// AmbiguousError reports a session ID prefix that matches more than one session.
type AmbiguousError struct {
	Prefix     string
	Candidates []Result
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches more than one session; use a longer prefix:", e.Prefix)
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, "\n  %s  %s  %s", c.SessionID, c.CWD, c.Title)
	}
	return b.String()
}

// Resolve maps a full session ID or a unique prefix of one to the full ID.
// Automated sessions resolve too: an explicit ID always wins over filters.
func (ix *Index) Resolve(idOrPrefix string) (string, error) {
	q := strings.TrimSpace(idOrPrefix)
	if q == "" {
		return "", fmt.Errorf("no session matches an empty ID")
	}
	var id string
	err := ix.db.QueryRow(`SELECT id FROM sessions WHERE id = ?`, q).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := ix.db.Query(`SELECT id, cwd, title FROM sessions WHERE id LIKE ? ESCAPE '\' ORDER BY mtime DESC LIMIT 11`,
		likePrefix(q))
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var cands []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.SessionID, &r.CWD, &r.Title); err != nil {
			return "", err
		}
		cands = append(cands, r)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(cands) {
	case 0:
		return "", fmt.Errorf("no session matches %q", q)
	case 1:
		return cands[0].SessionID, nil
	}
	if len(cands) > 10 {
		cands = cands[:10]
	}
	return "", &AmbiguousError{Prefix: q, Candidates: cands}
}

// likePrefix escapes LIKE wildcards in p and appends %.
func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

// Exchanges returns every indexed exchange of a session in order.
func (ix *Index) Exchanges(sessionID string) ([]Exchange, error) {
	rows, err := ix.db.Query(`SELECT seq, prompt, prose FROM chunks WHERE session_id = ? ORDER BY CAST(seq AS INTEGER)`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Exchange
	for rows.Next() {
		var e Exchange
		if err := rows.Scan(&e.Seq, &e.Prompt, &e.Prose); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
