package httpapi

import (
	"crypto/subtle"
	"easynr/internal/auth"
	"easynr/internal/registry"
	"easynr/internal/web"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cookieName = "easynr_session"

type server struct {
	auth    auth.Service
	engine  *registry.Engine
	limiter limiter
}

func New(s auth.Service, e *registry.Engine) (http.Handler, error) {
	if s.Store == nil || e == nil {
		return nil, fmt.Errorf("storage required")
	}
	a := &server{auth: s, engine: e}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/login", a.login)
	mux.Handle("/api/", a.protected())
	assets := web.Handler()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/-/ui/app.js", "/-/ui/style.css":
			assets.ServeHTTP(w, r)
		default:
			e.ServeHTTP(w, r)
		}
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	}), nil
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, status int, code, message string) {
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(v)
	if err == nil {
		var extra any
		if e := d.Decode(&extra); e != io.EOF {
			err = errors.New("extra JSON")
			var tooBig *http.MaxBytesError
			if errors.As(e, &tooBig) {
				err = e
			}
		}
	}
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			failure(w, 413, "body_too_large", "JSON body exceeds 64 KiB")
		} else {
			failure(w, 400, "invalid_json", "Provide one valid JSON object with known fields")
		}
		return false
	}
	return true
}
func (a *server) originOK(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || u.Host != r.Host || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || origin != u.Scheme+"://"+u.Host || (r.TLS != nil && u.Scheme != "https") {
		failure(w, 403, "invalid_origin", "Origin does not match request host")
		return false
	}
	return true
}
func (a *server) cookie(w http.ResponseWriter, r *http.Request, secret string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: secret, Path: "/", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(r.Header.Get("Origin"), "https://"), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}
func (a *server) login(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(w, r) {
		return
	}
	now := time.Now()
	if a.auth.Now != nil {
		now = a.auth.Now()
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if retry := a.limiter.allow(ip, now); retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		failure(w, 429, "rate_limited", "Too many login attempts; try again shortly")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	secret, session, err := a.auth.Login(in.Password)
	if errors.Is(err, auth.ErrUnauthorized) {
		failure(w, 401, "invalid_credentials", "Incorrect password")
		return
	}
	if err != nil {
		failure(w, 500, "internal_error", "Unable to create session")
		return
	}
	a.cookie(w, r, secret, session.Expires, 12*60*60)
	reply(w, 200, map[string]string{"csrfToken": session.CSRF})
}
func (a *server) protected() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(cookieName)
		s, err := a.auth.Session(c.Value)
		if err != nil {
			failure(w, 401, "unauthorized", "Sign in required")
			return
		}
		reply(w, 200, map[string]any{"authenticated": true, "csrfToken": s.CSRF})
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(cookieName)
		if err := a.auth.Logout(c.Value); err != nil {
			failure(w, 500, "internal_error", "Unable to end session")
			return
		}
		a.cookie(w, r, "", time.Unix(1, 0), -1)
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/tokens", a.listTokens)
	mux.HandleFunc("POST /api/tokens", a.createToken)
	mux.HandleFunc("DELETE /api/tokens/{id}", a.revokeToken)
	mux.HandleFunc("GET /api/packages", a.packages)
	mux.HandleFunc("GET /api/package", a.packageDetails)
	mux.HandleFunc("DELETE /api/package", a.removePackage)
	mux.HandleFunc("PUT /api/tag", a.updateTag)
	mux.HandleFunc("DELETE /api/tag", a.deleteTag)
	mux.HandleFunc("GET /api/cache", a.cacheStats)
	mux.HandleFunc("DELETE /api/cache", a.clearCache)
	mux.HandleFunc("GET /api/upstreams", a.upstreams)
	mux.HandleFunc("PUT /api/upstreams", a.saveUpstream)
	mux.HandleFunc("POST /api/upstreams/{id}/test", a.testUpstream)
	mux.HandleFunc("DELETE /api/upstreams/{id}", a.deleteUpstream)
	mux.HandleFunc("GET /api/routes", a.routes)
	mux.HandleFunc("PUT /api/routes", a.saveRoute)
	mux.HandleFunc("DELETE /api/routes", a.deleteRoute)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { failure(w, 404, "not_found", "Unknown API route") })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			failure(w, 401, "unauthorized", "Sign in required")
			return
		}
		s, err := a.auth.Session(c.Value)
		if err != nil {
			failure(w, 401, "unauthorized", "Session expired; sign in again")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !a.originOK(w, r) {
				return
			}
			csrf := r.Header.Get("X-CSRF-Token")
			if subtle.ConstantTimeCompare([]byte(csrf), []byte(s.CSRF)) != 1 {
				failure(w, 403, "invalid_csrf", "Session verification failed; refresh and retry")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

type window struct {
	start time.Time
	count int
}
type limiter struct {
	mu     sync.Mutex
	ips    map[string]window
	global window
}

func (l *limiter) allow(ip string, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ips == nil {
		l.ips = map[string]window{}
	}
	for k, v := range l.ips {
		if now.Sub(v.start) >= time.Minute {
			delete(l.ips, k)
		}
	}
	if now.Sub(l.global.start) >= time.Minute {
		l.global = window{start: now}
	}
	v, ok := l.ips[ip]
	if !ok {
		v = window{start: now}
	}
	wait := func(start time.Time) int { return max(1, int((time.Minute-now.Sub(start)+time.Second-1)/time.Second)) }
	if l.global.count >= 60 {
		return wait(l.global.start)
	}
	if v.count >= 5 {
		return wait(v.start)
	}
	if !ok && len(l.ips) >= 4096 {
		return 60
	}
	v.count++
	l.global.count++
	l.ips[ip] = v
	return 0
}
