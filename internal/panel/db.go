package panel

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type Row map[string]any

func (r Row) Int(k string) int64 {
	switch v := r[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

func (r Row) Float(k string) float64 {
	switch v := r[k].(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

func (r Row) Str(k string) string {
	switch v := r[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

func (r Row) Bool(k string) bool { return r.Int(k) != 0 }
func (r Row) Null(k string) bool { return r[k] == nil }

type Q interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

func all(q Q, query string, args ...any) []Row {
	rows, err := q.Query(query, args...)
	check(err)
	defer rows.Close()
	cols, err := rows.Columns()
	check(err)
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		check(rows.Scan(ptrs...))
		row := make(Row, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	check(rows.Err())
	return out
}

func one(q Q, query string, args ...any) Row {
	rows := all(q, query, args...)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func scalar(q Q, query string, args ...any) int64 {
	r := one(q, query, args...)
	for _, v := range r {
		return Row{"v": v}.Int("v")
	}
	return 0
}

func exec(q Q, query string, args ...any) sql.Result {
	res, err := q.Exec(query, args...)
	check(err)
	return res
}

func affected(res sql.Result) int64 {
	n, err := res.RowsAffected()
	check(err)
	return n
}

func lastID(res sql.Result) int64 {
	n, err := res.LastInsertId()
	check(err)
	return n
}

type DB struct{ *sql.DB }

func openDB(path string) *DB {
	if dir := filepath.Dir(path); dir != "" {
		check(os.MkdirAll(dir, 0o700))
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
	d, err := sql.Open("sqlite", dsn)
	check(err)
	d.SetMaxOpenConns(8)
	check(d.Ping())
	return &DB{d}
}

func (d *DB) tx(fn func(tx Q)) {
	t, err := d.Begin()
	check(err)
	done := false
	defer func() {
		if !done {
			_ = t.Rollback()
		}
	}()
	fn(t)
	check(t.Commit())
	done = true
}

func migrate(d *DB, fsys fs.FS) []string {
	exec(d, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)")
	done := map[string]bool{}
	for _, r := range all(d, "SELECT version FROM schema_migrations") {
		done[r.Str("version")] = true
	}
	files, err := fs.Glob(fsys, "migrations/*.sql")
	check(err)
	sort.Strings(files)
	var applied []string
	for _, f := range files {
		version := strings.TrimSuffix(filepath.Base(f), ".sql")
		if done[version] {
			continue
		}
		body, err := fs.ReadFile(fsys, f)
		check(err)
		d.tx(func(tx Q) {
			exec(tx, string(body))
			exec(tx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", version, now())
		})
		applied = append(applied, version)
	}
	return applied
}
