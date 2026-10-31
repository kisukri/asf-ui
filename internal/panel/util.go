package panel

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type M = map[string]any

type AppError struct {
	Status  int
	Code    string
	Message string
	Hint    string
	Fields  []string
	Extra   M
}

func (e *AppError) Error() string { return e.Message }

func (e *AppError) toMap() M {
	m := M{"code": e.Code, "message": e.Message}
	if e.Hint != "" {
		m["hint"] = e.Hint
	}
	if len(e.Fields) > 0 {
		m["fields"] = e.Fields
	}
	for k, v := range e.Extra {
		m[k] = v
	}
	return m
}

func newErr(status int, code, msg string, hint ...string) *AppError {
	e := &AppError{Status: status, Code: code, Message: msg}
	if len(hint) > 0 {
		e.Hint = hint[0]
	}
	return e
}

func fail(status int, code, msg string, hint ...string) {
	panic(newErr(status, code, msg, hint...))
}

func notFound(what string) *AppError {
	return newErr(404, "not_found", fmt.Sprintf("%s不存在，或你无权访问。", what))
}

func try(fn func()) (err *AppError) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*AppError); ok {
				err = e
				return
			}
			panic(r)
		}
	}()
	fn()
	return nil
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func now() int64 { return time.Now().Unix() }

var ctrlRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func stripCtrl(s string, repl string) string { return ctrlRe.ReplaceAllString(s, repl) }

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func truncate(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func joinInts(ids []int64, sep string) string {
	parts := make([]string, len(ids))
	for i, v := range ids {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, sep)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func errHint(e *AppError) string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\n" + e.Hint
}
