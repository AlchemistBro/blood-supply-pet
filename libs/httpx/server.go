// Package httpx — общий HTTP-каркас сервисов: сервер с мягкой остановкой, здоровье и готовность,
// middleware (корреляция, восстановление после panic, журнал доступа), ошибки в формате problem+json,
// клиент с таймаутом, повторами и предохранителем.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server — http.Server с таймаутами и мягкой остановкой.
type Server struct {
	srv *http.Server
	log *slog.Logger
}

// NewServer создаёт сервер. Таймауты заданы, чтобы медленный клиент не держал соединения вечно.
func NewServer(addr string, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
		log: log,
	}
}

// Run слушает до отмены контекста, затем даёт 15 секунд на завершение запросов.
func (s *Server) Run(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("прослушивание %s: %w", s.srv.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve обслуживает уже открытый слушатель: так вызывающий узнаёт фактический адрес (порт :0) до старта,
// например тестовый процесс, запускающий имитатор. Остановка — как у Run.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.log.Info("HTTP-сервер запущен", "addr", ln.Addr().String())
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(ln) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP-сервер: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("остановка HTTP-сервера: %w", err)
		}
		s.log.Info("HTTP-сервер остановлен")
		return nil
	}
}

// Addr возвращает адрес сервера.
func (s *Server) Addr() string { return s.srv.Addr }
