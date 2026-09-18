package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

type middleware func(http.Handler) http.Handler

// chain applies middlewares so that the first one listed is the outermost.
func chain(handler http.Handler, middlewares ...middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

type contextKey int

const (
	keyRequestID contextKey = iota
	keyClientIP
)

// RequestID returns the id given to the request (also sent back as X-Request-Id).
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)
	return id
}

// ClientIP returns the visitor's address. The service listens on loopback only and nginx is its
// only client, so X-Real-IP is trusted — but only when the connection really comes from loopback.
func ClientIP(ctx context.Context) net.IP {
	ip, _ := ctx.Value(keyClientIP).(net.IP)
	return ip
}

// ThroughProxy reports whether the request was forwarded by nginx (as opposed to a direct call
// on the server itself, e.g. the installer's health check).
func ThroughProxy(r *http.Request) bool {
	return r.Header.Get("X-Real-IP") != ""
}

func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote != nil && remote.IsLoopback() {
		if forwarded := net.ParseIP(r.Header.Get("X-Real-IP")); forwarded != nil {
			return forwarded
		}
	}
	return remote
}

func requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw [8]byte
		_, _ = rand.Read(raw[:])
		id := hex.EncodeToString(raw[:])
		w.Header().Set("X-Request-Id", id)

		ctx := context.WithValue(r.Context(), keyRequestID, id)
		ctx = context.WithValue(ctx, keyClientIP, clientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// securityHeaders are the defaults of everything the API serves itself; nginx adds its own set
// to the static site. API answers are never meant to be framed, sniffed or cached by accident.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}

func recoverPanics(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(recovered)
					}
					log.Error("panic in handler", "panic", recovered, "path", r.URL.Path, "stack", string(debug.Stack()))
					WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush keeps Server-Sent Events working through the recorder.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// accessLog writes one debug line per request. Visitors' addresses and query strings are left
// out on purpose: nginx keeps the request log, with its own retention (brief B6).
func accessLog(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			level := slog.LevelDebug
			if recorder.status >= 500 {
				level = slog.LevelError
			}
			log.Log(r.Context(), level, "request",
				"id", RequestID(r.Context()), "method", r.Method, "path", r.URL.Path,
				"status", recorder.status, "ms", time.Since(started).Milliseconds())
		})
	}
}
