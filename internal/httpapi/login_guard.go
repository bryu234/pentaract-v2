package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

type loginWindow struct {
	until time.Time
	count int
}
type loginGuard struct {
	mu      sync.Mutex
	entries map[string]loginWindow
	active  chan struct{}
	now     func() time.Time
}

func newLoginGuard() *loginGuard {
	return &loginGuard{entries: make(map[string]loginWindow), active: make(chan struct{}, 2), now: time.Now}
}
func (g *loginGuard) allow(key string, limit int, duration time.Duration) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for k, v := range g.entries {
		if !now.Before(v.until) {
			delete(g.entries, k)
		}
	}
	v, exists := g.entries[key]
	if !exists {
		if len(g.entries) >= 4096 {
			return false, 60
		}
		v = loginWindow{until: now.Add(duration)}
	}
	if v.count >= limit {
		return false, int(v.until.Sub(now).Seconds()) + 1
	}
	v.count++
	g.entries[key] = v
	return true, 0
}
func (g *loginGuard) accountBlocked(key string) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v := g.entries[key]
	now := g.now()
	return v.count >= 5 && now.Before(v.until), int(v.until.Sub(now).Seconds()) + 1
}
func (g *loginGuard) failure(key string) { g.allow(key, 5, 15*time.Minute) }
func (g *loginGuard) reset(key string)   { g.mu.Lock(); defer g.mu.Unlock(); delete(g.entries, key) }
func accountKey(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "account:" + hex.EncodeToString(h[:])
}
func (s *Server) clientIP(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	ip, e := netip.ParseAddr(host)
	if e != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	trusted := false
	for _, prefix := range s.Config.TrustedProxyCIDRs {
		if prefix.Contains(ip) {
			trusted = true
			break
		}
	}
	if trusted {
		if real, e := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); e == nil {
			ip = real.Unmap()
		}
	}
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}
func rateDenied(w http.ResponseWriter, seconds int) {
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	problem(w, 429, "Too many authentication attempts. Try again later.")
}
func (s *Server) protectLogin(g *loginGuard, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := g.allow("ip:"+s.clientIP(r), 10, time.Minute); !ok {
			rateDenied(w, wait)
			return
		}
		select {
		case g.active <- struct{}{}:
			defer func() { <-g.active }()
			next(w, r)
		default:
			rateDenied(w, 1)
		}
	}
}
