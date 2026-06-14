package ssr

import "testing"

func TestProductImageSrcsetsForCatalogProduct(t *testing.T) {
	imageURL := "/images/products/mango-sticky-rice-kit.jpg"

	if got, want := productImageWebPSrcset(imageURL), "/images/products/mango-sticky-rice-kit-320w.webp 320w, /images/products/mango-sticky-rice-kit-480w.webp 480w, /images/products/mango-sticky-rice-kit-720w.webp 720w, /images/products/mango-sticky-rice-kit-960w.webp 960w, /images/products/mango-sticky-rice-kit-1200w.webp 1200w"; got != want {
		t.Fatalf("productImageWebPSrcset = %q, want %q", got, want)
	}
	if got, want := productImageFallbackSrcset(imageURL), "/images/products/mango-sticky-rice-kit-320w.jpg 320w, /images/products/mango-sticky-rice-kit-480w.jpg 480w, /images/products/mango-sticky-rice-kit-720w.jpg 720w, /images/products/mango-sticky-rice-kit-960w.jpg 960w, /images/products/mango-sticky-rice-kit-1200w.jpg 1200w"; got != want {
		t.Fatalf("productImageFallbackSrcset = %q, want %q", got, want)
	}
}

func TestProductImageSrcsetsForPlaceholderFallback(t *testing.T) {
	if got, want := productImageSrc(""), "/images/placeholder-product.jpg"; got != want {
		t.Fatalf("productImageSrc empty = %q, want %q", got, want)
	}
	if got, want := productImageWebPSrcset(""), "/images/placeholder-product-320w.webp 320w, /images/placeholder-product-480w.webp 480w, /images/placeholder-product-720w.webp 720w, /images/placeholder-product-960w.webp 960w, /images/placeholder-product-1200w.webp 1200w"; got != want {
		t.Fatalf("productImageWebPSrcset empty = %q, want %q", got, want)
	}
}

func TestProductImageSrcsetsForNewNestedUpload(t *testing.T) {
	imageURL := "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.webp"

	if got, want := productImageWebPSrcset(imageURL), "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-320w.webp 320w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-480w.webp 480w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-720w.webp 720w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-960w.webp 960w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-1200w.webp 1200w"; got != want {
		t.Fatalf("productImageWebPSrcset nested upload = %q, want %q", got, want)
	}
	if got, want := productImageFallbackSrcset(imageURL), "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-320w.jpg 320w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-480w.jpg 480w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-720w.jpg 720w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-960w.jpg 960w, /images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original-1200w.jpg 1200w"; got != want {
		t.Fatalf("productImageFallbackSrcset nested upload = %q, want %q", got, want)
	}
}

func TestProductImageSrcsetsSkipURLsWithoutGeneratedSiblings(t *testing.T) {
	for _, imageURL := range []string{
		"https://cdn.example.com/products/mango.jpg",
		"/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111.jpg",
		"/images/products/mango-sticky-rice-kit-320w.jpg",
		"/images/categories/thai-snacks.jpg",
		"/images/products/mango-sticky-rice-kit.jpg?v=1",
		"/static/logo.svg",
	} {
		t.Run(imageURL, func(t *testing.T) {
			if got := productImageWebPSrcset(imageURL); got != "" {
				t.Fatalf("productImageWebPSrcset(%q) = %q, want empty", imageURL, got)
			}
			if got := productImageFallbackSrcset(imageURL); got != "" {
				t.Fatalf("productImageFallbackSrcset(%q) = %q, want empty", imageURL, got)
			}
		})
	}
}
