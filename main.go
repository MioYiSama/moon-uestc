package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/MioYiSama/moonuestc/internal/app"
	"github.com/MioYiSama/moonuestc/internal/school"
	"github.com/MioYiSama/moonuestc/internal/store"
	"github.com/MioYiSama/moonuestc/internal/web"
	"golang.org/x/sys/unix"
	auth "yyb_go/moonuestcauth"
)

func main() {
	unix.Umask(0077)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	password := os.Getenv("MOON_ADMIN_PASSWORD")
	if password == "" {
		return errors.New("MOON_ADMIN_PASSWORD is required")
	}
	dir := os.Getenv("MOON_DATA_DIR")
	if dir == "" {
		dir = "./data"
	}
	address := os.Getenv("MOON_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	secure := false
	if raw := os.Getenv("MOON_COOKIE_SECURE"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return errors.New("MOON_COOKIE_SECURE must be true or false")
		}
		secure = value
	}
	db, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("open persistent state: %w", err)
	}
	defer db.Close()
	authentication, err := auth.Open()
	if err != nil {
		return errors.New("open WeChat authentication cache failed")
	}
	defer authentication.Close()
	service := app.New(db, authentication, school.New("", nil), app.Options{})
	defer service.Close()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler:           web.New(service, password, secure),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	service.Start()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	log.Printf("moonuestc listening on %s; checkin 21:30 Asia/Shanghai", listener.Addr())
	select {
	case <-ctx.Done():
	case err = <-served:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	service.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
		_ = server.Close()
		if err == nil {
			err = shutdownErr
		}
	}
	service.Close()
	return err
}
