package httpapi

import (
	"github.com/bryu234/pentaract-v2/internal/config"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestLoginLimits(t *testing.T) {
	now := time.Now()
	g := newLoginGuard()
	g.now = func() time.Time { return now }
	key := accountKey("Owner@example.test")
	for i := 0; i < 5; i++ {
		if blocked, _ := g.accountBlocked(key); blocked {
			t.Fatal("early block")
		}
		g.failure(key)
	}
	if blocked, wait := g.accountBlocked(key); !blocked || wait <= 0 {
		t.Fatal("no block")
	}
	now = now.Add(16 * time.Minute)
	if blocked, _ := g.accountBlocked(key); blocked {
		t.Fatal("block never expired")
	}
	for i := 0; i < 10; i++ {
		if ok, _ := g.allow("ip", 10, time.Minute); !ok {
			t.Fatal("early IP block")
		}
	}
	if ok, _ := g.allow("ip", 10, time.Minute); ok {
		t.Fatal("IP limit bypass")
	}
	g.reset(key)
	if blocked, _ := g.accountBlocked(key); blocked {
		t.Fatal("success reset failed")
	}
}
func TestTrustedProxy(t *testing.T) {
	s := Server{Config: config.Config{TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("172.18.0.2/32")}}}
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("X-Real-IP", "198.51.100.1")
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	if s.clientIP(r) != "192.0.2.1" {
		t.Fatal("spoofed IP trusted")
	}
	r.RemoteAddr = "172.18.0.2:1234"
	if s.clientIP(r) != "198.51.100.1" {
		t.Fatal("proxy not recognized")
	}
}

func TestParallelPasswordLimit(t *testing.T) {
	g := newLoginGuard()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	s := &Server{}
	handler := s.protectLogin(g, func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release })
	for i := 0; i < 2; i++ {
		go func() { handler(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)); done <- struct{}{} }()
	}
	<-entered
	<-entered
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("parallel verification not limited")
	}
	close(release)
	<-done
	<-done
}
