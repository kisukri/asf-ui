package panel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

const usage = `usage: asf-panel <command>
  serve                       run the web server (default)
  gen-key                     print a new credential key
  migrate                     apply database migrations
  create-admin <name>         create an admin (asks for a password)
  create-invite [uses] [hrs]  create an invite code (shown once)
  rotate-keys                 re-encrypt stored ASF credentials with the primary key
  healthcheck                 check the local server (used by Docker)`

func Main(args []string, assets fs.FS) (code int) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*AppError); ok {
				fmt.Fprintln(os.Stderr, "error:", errHint(e))
			} else {
				fmt.Fprintln(os.Stderr, "error:", r)
			}
			code = 1
		}
	}()
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "-h", "--help", "help":
		fmt.Println(usage)
		return 0
	case "gen-key":
		fmt.Println(genKey())
		fmt.Fprintln(os.Stderr, "\nput this line first in CREDENTIAL_KEYS (comma separated; keep old keys until rotate-keys is done)")
		return 0
	}

	s, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 2
	}
	cfg = s
	level := slog.LevelInfo
	switch strings.ToUpper(cfg.LogLevel) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARNING", "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if cmd == "healthcheck" {
		return healthcheck()
	}
	if box, err = newCredentialBox(cfg.CredentialKeys); err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 2
	}
	loadCatalog(assets)
	db = openDB(cfg.DatabasePath)
	defer db.Close()
	if applied := migrate(db, assets); len(applied) > 0 {
		slog.Info("migrations applied", "versions", strings.Join(applied, ", "))
	}

	switch cmd {
	case "serve":
		static, err := fs.Sub(assets, "static")
		check(err)
		return serve(static)
	case "migrate":
		fmt.Println("database is up to date")
	case "create-admin":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "usage: asf-panel create-admin <name>")
			return 2
		}
		createAdmin(args[0])
	case "create-invite":
		uses, hours := 1, 72
		if len(args) > 0 {
			uses, _ = strconv.Atoi(args[0])
		}
		if len(args) > 1 {
			hours, _ = strconv.Atoi(args[1])
		}
		if uses < 1 || uses > 100 || hours < 1 || hours > 24*90 {
			fmt.Fprintln(os.Stderr, "uses must be 1-100 and hours 1-2160")
			return 2
		}
		code := newInviteCode()
		t := now()
		exec(db, "INSERT INTO invites (code_hash, code_hint, max_uses, expires_at, created_at) VALUES (?, ?, ?, ?, ?)",
			sha256Hex(code), "…"+code[len(code)-5:], uses, t+int64(hours)*3600, t)
		fmt.Printf("invite code (shown once): %s\nuses: %d, expires in %d hours\n", code, uses, hours)
	case "rotate-keys":
		n := 0
		db.tx(func(tx Q) {
			for _, r := range all(tx, "SELECT id, user_id, url_enc, password_enc FROM asf_connections") {
				uid := r.Int("user_id")
				u := box.Decrypt(r.Str("url_enc"), aad(uid, "url"))
				var pw any
				if !r.Null("password_enc") {
					pw = box.Encrypt(box.Decrypt(r.Str("password_enc"), aad(uid, "password")), aad(uid, "password"))
				}
				t := parseTarget(u, true)
				exec(tx, "UPDATE asf_connections SET url_enc = ?, password_enc = ?, url_fingerprint = ? WHERE id = ?",
					box.Encrypt(u, aad(uid, "url")), pw, box.Fingerprint(fmt.Sprintf("%s:%d", t.Host, t.Port)), r.Int("id"))
				n++
			}
		})
		fmt.Printf("re-encrypted %d connections. old keys can be removed from CREDENTIAL_KEYS now\n", n)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	return 0
}

func readPassword(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		check(err)
		return string(b)
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		check(err)
	}
	return strings.TrimRight(line, "\r\n")
}

func createAdmin(username string) {
	username = validateUsername(username)
	pw := readPassword("admin password: ")
	if term.IsTerminal(int(os.Stdin.Fd())) && pw != readPassword("again: ") {
		fail(400, "mismatch", "passwords do not match")
	}
	validateNewPassword(pw, username)
	h := hashPassword(pw)
	db.tx(func(tx Q) {
		if one(tx, "SELECT 1 FROM users WHERE username = ?", username) != nil {
			fail(409, "username_taken", "username already exists")
		}
		t := now()
		uid := lastID(exec(tx, `INSERT INTO users (username, password_hash, is_admin, is_active, created_at, password_changed_at)
			VALUES (?, ?, 1, 1, ?, ?)`, username, h, t, t))
		exec(tx, "INSERT INTO user_prefs (user_id, default_minutes) VALUES (?, 0)", uid)
	})
	fmt.Printf("admin %s created\n", username)
}

func serve(static fs.FS) int {
	handler := newRouter(static)
	sched.Start()
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr)
		errCh <- srv.ListenAndServe()
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	exitCode := 0
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			exitCode = 1
		}
	case <-sig:
		slog.Info("shutting down")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	sched.Stop()
	return exitCode
}

func healthcheck() int {
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 1
	}
	return 0
}
