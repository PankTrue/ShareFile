package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"sharefile/backend/internal/config"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/httpapi"
	"sharefile/backend/internal/logging"
	"sharefile/backend/internal/maintenance"
	"sharefile/backend/internal/metadata"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(os.Args[1:]); err != nil {
		slog.Error("sharefile stopped", "error", err)
		os.Exit(1)
	}
}
func run(args []string) (result error) {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve", "init-storage", "admin-create", "admin-reset", "backup", "restore":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("config", "/opt/etc/sharefile.json", "configuration path")
	username := flags.String("username", "", "administrator username")
	passwordStdin := flags.Bool("password-stdin", false, "read password from stdin for automation")
	archive := flags.String("archive", "", "backup archive path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if c.LogFile != "" {
		log, err := logging.Open(c.LogFile)
		if err != nil {
			return err
		}
		defer func() {
			if result != nil {
				slog.Error("command failed", "error", result)
			}
			log.Close()
			slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		}()
		slog.SetDefault(slog.New(slog.NewJSONHandler(log, nil)))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if command == "init-storage" {
		return files.Initialize(c.DataDir, c.VolumeID)
	}
	if command == "restore" {
		return maintenance.Restore(ctx, c.DataDir, c.VolumeID, *archive)
	}
	disk, err := files.OpenDisk(c.DataDir, c.VolumeID, c.ReserveBytes)
	if err != nil {
		return err
	}
	defer disk.Close()
	lock, err := disk.Root.OpenFile("metadata/process.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another process is using this storage; stop it before maintenance")
	}
	store, err := metadata.Open(filepath.Join(c.DataDir, "metadata", "sharefile.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	app, err := httpapi.New(c, store, disk)
	if err != nil {
		return err
	}
	if err = app.Files.Recover(ctx); err != nil {
		return err
	}
	switch command {
	case "admin-create":
		password, err := readPassword(*passwordStdin)
		if err != nil {
			return err
		}
		user, err := app.Identity.CreateUser(ctx, *username, password, true)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "administrator created:", user.Username)
		return nil
	case "admin-reset":
		password, err := readPassword(*passwordStdin)
		if err != nil {
			return err
		}
		var id string
		if err = store.DB.QueryRowContext(ctx, "SELECT id FROM users WHERE username=? AND admin=1", strings.ToLower(*username)).Scan(&id); err != nil {
			return fmt.Errorf("administrator not found")
		}
		active := true
		if err = app.Identity.UpdateUser(ctx, id, &active, password); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "administrator access reset")
		return nil
	case "backup":
		if _, err = store.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return err
		}
		if err = store.Close(); err != nil {
			return err
		}
		return maintenance.Backup(ctx, disk, *archive)
	case "serve":
		return serve(ctx, app)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
func readPassword(stdin bool) (string, error) {
	if stdin {
		text, err := bufio.NewReader(io.LimitReader(os.Stdin, 512)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"), nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("interactive terminal required; use -password-stdin for automation")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	confirm, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(b) != string(confirm) {
		return "", fmt.Errorf("passwords do not match")
	}
	return string(b), nil
}
func serve(ctx context.Context, a *httpapi.App) error {
	if err := a.Files.Cleanup(ctx); err != nil {
		return err
	}
	public, err := net.Listen("tcp", a.Config.PublicAddr)
	if err != nil {
		return err
	}
	private, err := net.Listen("tcp", a.Config.AdminAddr)
	if err != nil {
		public.Close()
		return err
	}
	public = &limitedListener{Listener: public, slots: make(chan struct{}, 64), closed: make(chan struct{})}
	private = &limitedListener{Listener: private, slots: make(chan struct{}, 16), closed: make(chan struct{})}
	makeServer := func(h http.Handler) *http.Server {
		return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	}
	pub, priv := makeServer(a.Public()), makeServer(a.Private())
	results := make(chan error, 2)
	go func() { results <- pub.Serve(public) }()
	go func() {
		if a.Config.InsecureLocal {
			results <- priv.Serve(private)
		} else {
			results <- priv.ServeTLS(private, a.Config.AdminCert, a.Config.AdminKey)
		}
	}()
	slog.Info("server started", "public", a.Config.PublicAddr, "admin", a.Config.AdminAddr)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var result error
	active := true
	for active {
		select {
		case <-ctx.Done():
			active = false
		case result = <-results:
			active = false
		case <-ticker.C:
			if err = a.Files.Disk.Check(); err != nil {
				result = err
				active = false
				continue
			}
			cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = a.Files.Cleanup(cleanup)
			cancel()
			if err != nil {
				slog.Error("cleanup failed", "error", err)
			}
		}
	}
	stop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err1, err2 := pub.Shutdown(stop), priv.Shutdown(stop)
	if err1 != nil {
		_ = pub.Close()
	}
	if err2 != nil {
		_ = priv.Close()
	}
	if result != nil && !errors.Is(result, http.ErrServerClosed) {
		return result
	}
	return errors.Join(err1, err2)
}

type limitedListener struct {
	net.Listener
	slots  chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.closed:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
