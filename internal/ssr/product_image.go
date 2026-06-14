package ssr

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
)

const (
	productImageRenderedWidth  = "1200"
	productImageRenderedHeight = "900"
)

var (
	productImageResponsiveWidths = []int{320, 480, 720, 960, 1200}
	productImageDerivativeName   = regexp.MustCompile(`-\d+w$`)
	productImageUploadUUID       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func productImageSrc(imageURL string) string {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return catalog.DefaultProductImagePlaceholderURL
	}
	return imageURL
}

func productImageWebPSrcset(imageURL string) string {
	return productImageSrcset(imageURL, "webp")
}

func productImageFallbackSrcset(imageURL string) string {
	return productImageSrcset(imageURL, "jpg")
}

func productImageSrcset(imageURL string, extension string) string {
	src := productImageSrc(imageURL)
	if !responsiveProductImageEligible(src) {
		return ""
	}

	base := strings.TrimSuffix(src, path.Ext(src))
	entries := make([]string, 0, len(productImageResponsiveWidths))
	for _, width := range productImageResponsiveWidths {
		entries = append(entries, fmt.Sprintf("%s-%dw.%s %dw", base, width, extension, width))
	}
	return strings.Join(entries, ", ")
}

func responsiveProductImageEligible(imageURL string) bool {
	if imageURL == "" || strings.ContainsAny(imageURL, "?#") || strings.Contains(imageURL, "://") {
		return false
	}
	if imageURL == catalog.DefaultProductImagePlaceholderURL {
		return supportedProductImageExtension(imageURL) && !productImageDerivativeName.MatchString(strings.TrimSuffix(path.Base(imageURL), path.Ext(imageURL)))
	}
	if strings.HasPrefix(imageURL, "/images/products/uploads/") {
		return responsiveUploadedProductImageEligible(imageURL)
	}
	if !strings.HasPrefix(imageURL, "/images/products/") {
		return false
	}
	if !supportedProductImageExtension(imageURL) {
		return false
	}

	basename := strings.TrimSuffix(path.Base(imageURL), path.Ext(imageURL))
	return !productImageDerivativeName.MatchString(basename)
}

func supportedProductImageExtension(imageURL string) bool {
	extension := strings.ToLower(path.Ext(imageURL))
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func responsiveUploadedProductImageEligible(imageURL string) bool {
	if !supportedProductImageExtension(imageURL) {
		return false
	}
	remainder := strings.TrimPrefix(imageURL, "/images/products/uploads/")
	parts := strings.Split(remainder, "/")
	if len(parts) != 4 || !validProductImageUploadYearMonth(parts[0], parts[1]) || !productImageUploadUUID.MatchString(parts[2]) {
		return false
	}
	return strings.TrimSuffix(parts[3], path.Ext(parts[3])) == "original"
}

func validProductImageUploadYearMonth(year string, month string) bool {
	if len(year) != 4 || len(month) != 2 {
		return false
	}
	for _, char := range year + month {
		if char < '0' || char > '9' {
			return false
		}
	}
	monthValue, err := strconv.Atoi(month)
	return err == nil && monthValue >= 1 && monthValue <= 12
}
