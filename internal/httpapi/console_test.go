package httpapi

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestConsolePublicShellDoesNotResolveCredentialsOrReadBusinessData(t *testing.T) {
	t.Setenv("CONSOLE_TEST_ADMIN", "private-admin-canary")
	t.Setenv("CONSOLE_TEST_INTERNAL", "private-service-canary")
	a := &App{TenantID: "public-tenant", AdminTokenEnv: "CONSOLE_TEST_ADMIN", InternalTokenEnv: "CONSOLE_TEST_INTERNAL"}
	mux := http.NewServeMux()
	a.registerConsole(mux)

	w := appRequest(mux, http.MethodGet, "/admin", "", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("public shell: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `data-tenant="public-tenant"`) {
		t.Fatal("shell must bind its nonsecret tenant identifier")
	}
	for _, secret := range []string{"private-admin-canary", "private-service-canary", a.AdminTokenEnv, a.InternalTokenEnv} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("credential or credential reference leaked into public shell: %q", secret)
		}
	}
	// A nil Store is intentional: serving this public shell cannot read a Bot,
	// its instructions, business records, or account credential references.
}

func TestConsoleTenantIsContextuallyEscaped(t *testing.T) {
	tenant := `"><script>alert("tenant")</script><img src=x onerror=alert(1)>&'`
	a := &App{TenantID: tenant}
	mux := http.NewServeMux()
	a.registerConsole(mux)
	w := appRequest(mux, http.MethodGet, "/admin", "", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("shell: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, tenant) || strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Fatal("tenant identifier became executable HTML")
	}
	attribute := regexp.MustCompile(`data-tenant="([^"]*)"`).FindStringSubmatch(body)
	if len(attribute) != 2 || html.UnescapeString(attribute[1]) != tenant {
		t.Fatalf("escaped tenant must round-trip as one attribute: %q", attribute)
	}
	label := regexp.MustCompile(`<strong id="tenant-label">([^<]*)</strong>`).FindStringSubmatch(body)
	if len(label) != 2 || html.UnescapeString(label[1]) != tenant {
		t.Fatalf("escaped tenant must round-trip as text: %q", label)
	}
}

func TestConsoleFixedRoutesHeadersAndHead(t *testing.T) {
	a := &App{TenantID: "tenant"}
	mux := http.NewServeMux()
	a.registerConsole(mux)
	for _, route := range []struct{ path, contentType string }{
		{"/admin", "text/html; charset=utf-8"},
		{"/admin/app.js", "text/javascript; charset=utf-8"},
		{"/admin/style.css", "text/css; charset=utf-8"},
	} {
		t.Run(route.path, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				w := appRequest(mux, method, route.path, "", "", "")
				if w.Code != http.StatusOK || w.Header().Get("Content-Type") != route.contentType {
					t.Fatalf("%s: %d %v", method, w.Code, w.Header())
				}
				for header, want := range map[string]string{
					"Cache-Control":                "no-store",
					"X-Content-Type-Options":       "nosniff",
					"X-Frame-Options":              "DENY",
					"Referrer-Policy":              "no-referrer",
					"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
					"Cross-Origin-Resource-Policy": "same-origin",
				} {
					if got := w.Header().Get(header); got != want {
						t.Errorf("%s %s = %q, want %q", method, header, got, want)
					}
				}
				csp := w.Header().Get("Content-Security-Policy")
				for _, directive := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "base-uri 'none'", "frame-ancestors 'none'", "object-src 'none'"} {
					if !strings.Contains(csp, directive) {
						t.Errorf("CSP missing %q: %s", directive, csp)
					}
				}
				if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
					t.Fatalf("CSP permits executable inline content: %s", csp)
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Fatalf("HEAD returned a body: %d bytes", w.Body.Len())
				}
				if method == http.MethodGet && w.Body.Len() == 0 {
					t.Fatal("GET returned an empty document")
				}
			}
		})
	}
	for _, path := range []string{"/admin/", "/admin/index.html", "/admin/console.go", "/admin/app.js/", "/admin/unknown.css"} {
		if w := appRequest(mux, http.MethodGet, path, "", "", ""); w.Code != http.StatusNotFound {
			t.Errorf("unregistered console path %q returned %d", path, w.Code)
		}
	}
	for _, path := range []string{"/admin", "/admin/app.js", "/admin/style.css"} {
		if w := appRequest(mux, http.MethodPost, path, "", "", ""); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %q returned %d", path, w.Code)
		}
	}
}

func TestConsoleUnconfiguredTenantFailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	(&App{}).registerConsole(mux)
	w := appRequest(mux, http.MethodGet, "/admin", "", "", "")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unconfigured shell: %d %v", w.Code, w.Header())
	}
	if strings.Contains(w.Body.String(), "console-root") {
		t.Fatal("unconfigured tenant must not render an active shell")
	}
}

func TestConsoleRegistrationPreservesAdministratorBoundary(t *testing.T) {
	_, store, mux := newTestApp(t)
	if w := appRequest(mux, http.MethodGet, "/admin", "", "", ""); w.Code != http.StatusOK {
		t.Fatalf("registered shell: %d", w.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/tenants/tenant/bots/bot", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || store.getBotCalls != 0 {
		t.Fatalf("public console weakened business authentication: %d, store calls %d", response.Code, store.getBotCalls)
	}
	if strings.Contains(response.Body.String(), store.bot.Config.Instructions) {
		t.Fatal("unauthenticated business request exposed Bot configuration")
	}
}
