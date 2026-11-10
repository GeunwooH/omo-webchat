package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newStaticTestServer() http.Handler {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return securityHeaders(s.staticHandler())
}

func serveStatic(t *testing.T, h http.Handler, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticServesAppShellAndLocaleRoutes(t *testing.T) {
	h := newStaticTestServer()
	for _, target := range []string{"/", "/login", "/ko/login", "/en/", "/ko"} {
		rec := serveStatic(t, h, target, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Fatalf("%s: status %d, want the SPA shell with 200", target, rec.Code)
		}
	}
}

func TestStaticServesDocumentLanguageForLocalePrefix(t *testing.T) {
	h := newStaticTestServer()
	for target, want := range map[string]string{"/ko/login": "ko", "/ko": "ko", "/en/login": "en", "/login": "en", "/": "en"} {
		rec := serveStatic(t, h, target, nil)
		if !strings.Contains(rec.Body.String(), `<html lang="`+want+`">`) {
			t.Fatalf("%s: want <html lang=%q> in the served shell", target, want)
		}
	}
}

func TestStaticReturnsRealNotFoundForMisses(t *testing.T) {
	h := newStaticTestServer()
	for _, target := range []string{"/robots.txt", "/assets/missing-abc123.js", "/assets/", "/assets", "/nonexistent", "/ko/missing.js"} {
		rec := serveStatic(t, h, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Fatalf("%s: served the SPA shell for a miss", target)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control %q, want no-store", target, got)
		}
	}
}

func TestStaticSetsSecurityHeaders(t *testing.T) {
	rec := serveStatic(t, newStaticTestServer(), "/", nil)
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy %q lacks frame-ancestors 'none'", csp)
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS must not be sent without a TLS-terminating proxy")
	}
}

func TestStaticRedirectsForwardedPlainHTTPAndSendsHSTSOverHTTPS(t *testing.T) {
	h := newStaticTestServer()
	plain := serveStatic(t, h, "http://omo.example/ko/login?x=1", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") })
	if plain.Code != http.StatusPermanentRedirect || plain.Header().Get("Location") != "https://omo.example/ko/login?x=1" {
		t.Fatalf("plain HTTP: status %d Location %q", plain.Code, plain.Header().Get("Location"))
	}
	secure := serveStatic(t, h, "/", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") })
	if secure.Code != http.StatusOK || !strings.HasPrefix(secure.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Fatalf("https: status %d HSTS %q", secure.Code, secure.Header().Get("Strict-Transport-Security"))
	}
}

func TestStaticRevalidatesEmbeddedFilesWithETag(t *testing.T) {
	h := newStaticTestServer()
	first := serveStatic(t, h, "/favicon-32x32.png", nil)
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || tag == "" {
		t.Fatalf("status %d ETag %q, want 200 with an ETag", first.Code, tag)
	}
	again := serveStatic(t, h, "/favicon-32x32.png", func(r *http.Request) { r.Header.Set("If-None-Match", tag) })
	if again.Code != http.StatusNotModified {
		t.Fatalf("revalidation status %d, want 304", again.Code)
	}
}
