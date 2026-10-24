package web

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

var RealIPHeader string

type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, hits: make(map[string]*bucket)}
}

func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10_000 {
		for k, b := range l.hits {
			if !now.Before(b.reset) {
				delete(l.hits, k)
			}
		}
	}
	b := l.hits[key]
	if b == nil || !now.Before(b.reset) {
		b = &bucket{reset: now.Add(l.window)}
		l.hits[key] = b
	}
	if b.count >= l.limit {
		return false, b.reset.Sub(now)
	}
	b.count++
	return true, 0
}

var (
	loginLimit    = newLimiter(20, 15*time.Minute)
	registerLimit = newLimiter(5, time.Hour)
	forgotLimit   = newLimiter(5, time.Hour)
	forgotPerMail = newLimiter(3, time.Hour)
	recoverLimit  = newLimiter(10, 15*time.Minute)
	verifyPerUser = newLimiter(3, time.Hour)
)

func init() {
	use("/login", rateLimit(loginLimit))
	use("/api/v0/login", rateLimit(loginLimit))
	use("/register", rateLimit(registerLimit))
	use("/forgot", rateLimit(forgotLimit))
	use("/recover", rateLimit(recoverLimit))
}

func rateLimit(l *limiter) middlewareFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
		if r.Method != http.MethodPost {
			return ctx
		}
		if ok, wait := l.allow(clientIP(r), time.Now()); !ok {
			tooManyRequests(w, wait)
			return nil
		}
		return ctx
	}
}

func tooManyRequests(w http.ResponseWriter, wait time.Duration) {
	minutes := int(wait.Minutes()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	http.Error(w, "Too many attempts. Try again in "+strconv.Itoa(minutes)+" minutes.", http.StatusTooManyRequests)
}

func clientIP(r *http.Request) string {
	raw := r.RemoteAddr
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	if RealIPHeader != "" {
		if v := r.Header.Get(RealIPHeader); v != "" {
			raw = strings.TrimSpace(v[strings.LastIndexByte(v, ',')+1:])
		}
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return raw
	}
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, _ := addr.Prefix(64)
		return prefix.String()
	}
	return addr.String()
}
