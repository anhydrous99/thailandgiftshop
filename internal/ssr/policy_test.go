package ssr

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestPolicyPagesRenderWithExpectedContentAndIndexing(t *testing.T) {
	handler := NewHandler(nil)
	tests := []struct {
		path      string
		wantTitle string
		wantBody  []string
		noindex   bool
		draft     bool
	}{
		{path: "/shipping", wantTitle: "Shipping | Thailand Gift Shop", wantBody: []string{"ships within the United States", "Shipping is free"}},
		{path: "/returns", wantTitle: "Returns and refunds | Thailand Gift Shop", wantBody: []string{"returned in full to your original payment method"}},
		{path: "/contact", wantTitle: "Contact us | Thailand Gift Shop", wantBody: []string{`href="mailto:support@thailandgiftshop.com"`, "two business days"}},
		{path: "/privacy", wantTitle: "Privacy Policy | Thailand Gift Shop", wantBody: []string{"never see or store your card number"}, noindex: true, draft: true},
		{path: "/terms", wantTitle: "Terms of Service | Thailand Gift Shop", wantBody: []string{"All prices are in US dollars"}, noindex: true, draft: true},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if !strings.Contains(response.Body, "<title>"+test.wantTitle+"</title>") {
				t.Fatalf("body missing title %q: %q", test.wantTitle, response.Body)
			}
			for _, want := range test.wantBody {
				if !strings.Contains(response.Body, want) {
					t.Fatalf("body missing %q", want)
				}
			}
			if cc := response.Headers["Cache-Control"]; cc != catalogPageCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", cc, catalogPageCacheControl)
			}
			robots := response.Headers["X-Robots-Tag"]
			metaNoindex := strings.Contains(response.Body, `<meta name="robots" content="noindex, follow">`)
			draftNotice := strings.Contains(response.Body, `data-testid="policy-draft-notice"`)
			if test.noindex {
				if robots != "noindex, follow" {
					t.Fatalf("X-Robots-Tag = %q, want noindex, follow", robots)
				}
				if !metaNoindex {
					t.Fatalf("draft page %q missing noindex meta", test.path)
				}
			} else {
				if robots != "" {
					t.Fatalf("X-Robots-Tag = %q, want empty (indexable)", robots)
				}
				if metaNoindex {
					t.Fatalf("indexable page %q must not be noindex", test.path)
				}
			}
			if draftNotice != test.draft {
				t.Fatalf("draft notice present = %v, want %v for %q", draftNotice, test.draft, test.path)
			}
		})
	}
}

func TestPolicyTrailingSlashRedirects(t *testing.T) {
	handler := NewHandler(nil)
	for path, want := range map[string]string{
		"/shipping/": "/shipping",
		"/returns/":  "/returns",
		"/privacy/":  "/privacy",
		"/terms/":    "/terms",
		"/contact/":  "/contact",
	} {
		response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
		if err != nil {
			t.Fatalf("Handle(%q) returned error: %v", path, err)
		}
		if response.StatusCode != http.StatusPermanentRedirect {
			t.Fatalf("status for %q = %d, want %d", path, response.StatusCode, http.StatusPermanentRedirect)
		}
		if got := response.Headers["Location"]; got != want {
			t.Fatalf("Location for %q = %q, want %q", path, got, want)
		}
	}
}

func TestPolicyAndContactLinkedFromFooter(t *testing.T) {
	handler := NewHandler(nil)
	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	for _, want := range []string{`href="/shipping"`, `href="/returns"`, `href="/contact"`, `href="/privacy"`, `href="/terms"`, `href="mailto:support@thailandgiftshop.com"`} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("home footer missing %q", want)
		}
	}
}

func TestSitemapIncludesIndexablePoliciesOnly(t *testing.T) {
	handler := NewHandler(nil)
	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/sitemap.xml"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	for _, want := range []string{canonicalHost + "/shipping", canonicalHost + "/returns", canonicalHost + "/contact"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("sitemap missing %q", want)
		}
	}
	for _, omit := range []string{canonicalHost + "/privacy", canonicalHost + "/terms"} {
		if strings.Contains(response.Body, omit) {
			t.Fatalf("sitemap must omit draft/noindex page %q", omit)
		}
	}
}
