package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/ssr"
)

const devserverTestCartSecret = "devserver-cart-test-secret"

func TestNewDevServerHandlerUsesDemoStoreWhenEnabled(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "1")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	store, ok := handler.Catalog().(*catalog.MemoryStore)
	if !ok {
		t.Fatalf("handler catalog store = %T, want *catalog.MemoryStore", handler.Catalog())
	}

	products, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if len(products) != 11 {
		t.Fatalf("active demo product count = %d, want 11", len(products))
	}
}

func TestNewDevServerHandlerKeepsEnvironmentStoreByDefault(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	if _, ok := handler.Catalog().(catalog.EmptyStore); !ok {
		t.Fatalf("handler catalog store = %T, want catalog.EmptyStore", handler.Catalog())
	}
}

func TestNewDevServerHandlerRequiresExactDemoStoreFlag(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "true")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	if _, ok := handler.Catalog().(catalog.EmptyStore); !ok {
		t.Fatalf("handler catalog store = %T, want catalog.EmptyStore", handler.Catalog())
	}
}

func TestDevserverForwardsPostBodyAndRequestCookies(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, devserverTestCartSecret)
	handler := ssr.NewHandler(devserverCartStore())
	encoded, err := cart.EncodeCookie(mustDevserverCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}}), devserverTestCartSecret)
	if err != nil {
		t.Fatalf("EncodeCookie returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/cart/items", strings.NewReader("slug=mango-sticky-rice-kit&quantity=2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: cart.CookieName, Value: encoded})
	responseRecorder := httptest.NewRecorder()

	handleSSR(handler, responseRecorder, request)

	response := responseRecorder.Result()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	setCookie := response.Header.Get("Set-Cookie")
	decoded := cart.DecodeCookie(devserverCookieValue(t, setCookie), devserverTestCartSecret)
	if decoded.NeedsClear {
		t.Fatal("decoded response cart NeedsClear = true, want false")
	}
	lines := decoded.Cart.Lines()
	if len(lines) != 1 || lines[0].Slug != "mango-sticky-rice-kit" || lines[0].Quantity != 3 {
		t.Fatalf("cart lines = %#v, want forwarded cookie quantity plus POST body quantity", lines)
	}
}

func TestDevserverWritesApiResponseCookies(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, devserverTestCartSecret)
	handler := ssr.NewHandler(devserverCartStore())
	request := httptest.NewRequest(http.MethodPost, "/cart/items", strings.NewReader("slug=mango-sticky-rice-kit&quantity=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	responseRecorder := httptest.NewRecorder()

	handleSSR(handler, responseRecorder, request)

	setCookies := responseRecorder.Result().Header.Values("Set-Cookie")
	if len(setCookies) != 1 {
		t.Fatalf("Set-Cookie headers = %#v, want one", setCookies)
	}
	if !strings.Contains(setCookies[0], cart.CookieName+"=") || !strings.Contains(setCookies[0], "HttpOnly") {
		t.Fatalf("Set-Cookie = %q, want serialized cart cookie", setCookies[0])
	}
}

func devserverCartStore() catalog.Store {
	return catalog.NewMemoryStore([]catalog.Product{
		{
			Slug:          "mango-sticky-rice-kit",
			Name:          "Mango Sticky Rice Treats",
			Status:        catalog.StatusActive,
			StockQuantity: 5,
		},
	}, nil)
}

func mustDevserverCart(t *testing.T, lines []cart.Line) cart.Cart {
	t.Helper()
	testCart, err := cart.New(lines)
	if err != nil {
		t.Fatalf("cart.New returned error: %v", err)
	}
	return testCart
}

func devserverCookieValue(t *testing.T, cookieHeader string) string {
	t.Helper()
	for part := range strings.SplitSeq(cookieHeader, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name == cart.CookieName {
			return value
		}
	}
	t.Fatalf("cart cookie missing from %q", cookieHeader)
	return ""
}
