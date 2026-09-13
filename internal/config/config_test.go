package config

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/cors"
)

func TestFrontendOrigins(t *testing.T) {
	origins := frontendOrigins("https://studio.example.com/, https://preview.pages.dev,http://localhost:3000,https://*,https://example.com/path,http://public.example.com")
	if len(origins) != 3 {
		t.Fatalf("expected three exact origins, got %v", origins)
	}
	cfg := &Config{FrontendOrigins: origins}
	handler := cors.Handler(cors.Options{
		AllowOriginFunc:  func(r *http.Request, origin string) bool { return cfg.AllowsFrontendOrigin(origin) },
		AllowedMethods:   []string{"GET", "POST"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, test := range []struct {
		origin  string
		allowed bool
	}{
		{"https://studio.example.com", true},
		{"https://preview.pages.dev", true},
		{"http://localhost:3000", true},
		{"https://untrusted.example.com", false},
		{"https://studio.example.com.attacker.test", false},
	} {
		t.Run(test.origin, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if allowed := response.Header().Get("Access-Control-Allow-Origin") == test.origin; allowed != test.allowed {
				t.Fatalf("origin allowed = %v, want %v", allowed, test.allowed)
			}
		})
	}
}

func TestInvalidFrontendOriginsFailClosed(t *testing.T) {
	cfg := &Config{FrontendOrigins: frontendOrigins("https://*,invalid")}
	if cfg.AllowsFrontendOrigin("https://untrusted.example.com") {
		t.Fatal("an invalid allowlist must deny every origin")
	}
}
