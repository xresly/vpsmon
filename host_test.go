package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsDomainHost(t *testing.T) {
	domains := []string{
		"nuts.ipv64.net", "nuts.ipv64.net:18080", "example.com",
		"sub.example.co.uk:443", "EXAMPLE.com.",
	}
	local := []string{
		"", "localhost", "localhost:18080", "127.0.0.1", "127.0.0.1:18080",
		"192.168.1.10:18080", "10.0.0.5", "[::1]", "[::1]:18080",
		"[2001:db8::1]:18080", "myserver", "nas.local", "printer.lan",
		"host.home", "db.internal",
	}
	for _, h := range domains {
		if !isDomainHost(h) {
			t.Errorf("isDomainHost(%q) = false, want true", h)
		}
	}
	for _, h := range local {
		if isDomainHost(h) {
			t.Errorf("isDomainHost(%q) = true, want false", h)
		}
	}
}

func TestPlainHandlerRedirect(t *testing.T) {
	app := &App{sessions: map[string]*session{}, fails: map[string][]time.Time{}}
	h := app.plainHandler()

	// domain host -> 308 to the https URL, path and query preserved
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://nuts.ipv64.net/login?next=/", nil))
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("domain host: got %d, want 308", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://nuts.ipv64.net/login?next=/" {
		t.Errorf("Location = %q", loc)
	}

	// IP host -> served by the app (the login page), no redirect
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:18080/login", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("IP host: got %d, want 200", rec.Code)
	}
}
