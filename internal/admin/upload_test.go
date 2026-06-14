package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const testUploadUUID = "11111111-1111-4111-8111-111111111111"
const testUploadOriginalJPG = "images/products/uploads/2026/06/" + testUploadUUID + "/original.jpg"
const testUploadOriginalPNG = "images/products/uploads/2026/06/" + testUploadUUID + "/original.png"
const testUploadOriginalWebP = "images/products/uploads/2026/06/" + testUploadUUID + "/original.webp"

var (
	testJPEGBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x48}
	testPNGBytes  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R'}
	testWebPBytes = []byte{'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}
)

func productImageConfirmVariantsForTest(originalKey string) []productImageConfirmVariant {
	variants := make([]productImageConfirmVariant, 0, len(productImageUploadVariantWidths)*2)
	for _, width := range productImageUploadVariantWidths {
		variants = append(variants,
			productImageConfirmVariant{Key: productImageVariantUploadKey(originalKey, width, "webp"), ContentType: "image/webp", SizeBytes: 1024, Width: width},
			productImageConfirmVariant{Key: productImageVariantUploadKey(originalKey, width, "jpg"), ContentType: "image/jpeg", SizeBytes: 1024, Width: width},
		)
	}
	return variants
}

func recordingImageSetObjectClient(originalKey string, originalContentType string, originalBody []byte) *recordingObjectClient {
	outputs := map[string]*s3.HeadObjectOutput{
		originalKey: &s3.HeadObjectOutput{ContentType: aws.String(originalContentType), ContentLength: aws.Int64(1024)},
	}
	bodies := map[string][]byte{originalKey: originalBody}
	for _, variant := range productImageConfirmVariantsForTest(originalKey) {
		outputs[variant.Key] = &s3.HeadObjectOutput{ContentType: aws.String(variant.ContentType), ContentLength: aws.Int64(variant.SizeBytes)}
		if variant.ContentType == "image/webp" {
			bodies[variant.Key] = testWebPBytes
		} else {
			bodies[variant.Key] = testJPEGBytes
		}
	}
	return &recordingObjectClient{outputs: outputs, bodies: bodies}
}

func TestUploadPresignRequiresAuthenticatedSessionBeforeProducingPolicy(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	uploads := &fakeProductImageUploads{}
	handler.uploads = uploads

	response, err := handler.Handle(context.Background(), adminJSONRequest(http.MethodPost, productImagePresignPath, map[string]any{
		"content_type": "image/jpeg",
		"size_bytes":   1024,
	}))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/admin/login" {
		t.Fatalf("response = (%d, %q), want redirect to login", response.StatusCode, response.Headers["Location"])
	}
	if uploads.presignCalls != 0 {
		t.Fatalf("presign calls = %d, want 0 before authentication", uploads.presignCalls)
	}
	if strings.Contains(response.Body, "policy") || strings.Contains(response.Body, "url") {
		t.Fatalf("unauthenticated body leaked upload output: %q", response.Body)
	}
	assertNoStore(t, response)
}

func TestUploadConfirmRequiresAuthenticatedSessionBeforeHeadObject(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	uploads := &fakeProductImageUploads{}
	handler.uploads = uploads

	response, err := handler.Handle(context.Background(), adminJSONRequest(http.MethodPost, productImageConfirmPath, map[string]any{
		"key":          testUploadOriginalJPG,
		"content_type": "image/jpeg",
		"size_bytes":   1024,
	}))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/admin/login" {
		t.Fatalf("response = (%d, %q), want redirect to login", response.StatusCode, response.Headers["Location"])
	}
	if uploads.confirmCalls != 0 {
		t.Fatalf("confirm calls = %d, want 0 before authentication", uploads.confirmCalls)
	}
	if strings.Contains(response.Body, "/images/products/uploads/") {
		t.Fatalf("unauthenticated body leaked trusted URL: %q", response.Body)
	}
	assertNoStore(t, response)
}

func TestUploadPresignRequiresValidCSRFBeforeProducingPolicy(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	uploads := &fakeProductImageUploads{}
	handler.uploads = uploads

	missingCSRF := authenticatedUploadRequest(t, handler, productImagePresignPath, map[string]any{
		"content_type": "image/png",
		"size_bytes":   1024,
	}, "")
	missingResponse, err := handler.Handle(context.Background(), missingCSRF)
	if err != nil {
		t.Fatalf("Handle missing CSRF returned error: %v", err)
	}
	if missingResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", missingResponse.StatusCode, http.StatusForbidden)
	}

	invalidCSRF := authenticatedUploadRequest(t, handler, productImagePresignPath, map[string]any{
		"content_type": "image/png",
		"size_bytes":   1024,
	}, "invalid")
	invalidResponse, err := handler.Handle(context.Background(), invalidCSRF)
	if err != nil {
		t.Fatalf("Handle invalid CSRF returned error: %v", err)
	}
	if invalidResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid CSRF status = %d, want %d", invalidResponse.StatusCode, http.StatusForbidden)
	}
	if uploads.presignCalls != 0 {
		t.Fatalf("presign calls = %d, want 0 before valid CSRF", uploads.presignCalls)
	}
}

func TestUploadConfirmRequiresValidCSRFBeforeHeadObject(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	uploads := &fakeProductImageUploads{}
	handler.uploads = uploads

	request := authenticatedUploadRequest(t, handler, productImageConfirmPath, map[string]any{
		"key":          testUploadOriginalWebP,
		"content_type": "image/webp",
		"size_bytes":   1024,
	}, "")
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	if uploads.confirmCalls != 0 {
		t.Fatalf("confirm calls = %d, want 0 before valid CSRF", uploads.confirmCalls)
	}
}

func TestUploadPresignAllowsJPEGPNGAndWebPOnly(t *testing.T) {
	for _, contentType := range []string{"image/jpeg", "image/png", "image/webp"} {
		t.Run(contentType, func(t *testing.T) {
			handler, _ := newAuthTestHandler(t)
			uploads := &fakeProductImageUploads{presignResponse: productImagePresignResponse{
				URL:       "https://product-images.example.s3.amazonaws.com",
				Fields:    map[string]string{"policy": "signed-policy", "Content-Type": contentType},
				Key:       "images/products/uploads/2026/06/" + testUploadUUID + "/original." + allowedProductImageTypes[contentType],
				ExpiresAt: time.Date(2026, 6, 7, 12, 5, 0, 0, time.UTC),
			}}
			handler.uploads = uploads

			request := authenticatedUploadRequest(t, handler, productImagePresignPath, map[string]any{
				"content_type": contentType,
				"size_bytes":   1024,
			}, validUploadCSRF)
			response, err := handler.Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusOK, response.Body)
			}
			assertNoStore(t, response)
			if uploads.presignCalls != 1 {
				t.Fatalf("presign calls = %d, want 1", uploads.presignCalls)
			}
			var body map[string]any
			decodeJSONResponse(t, response, &body)
			if body["url"] == "" || body["key"] == "" || body["fields"] == nil || body["expires_at"] == "" {
				t.Fatalf("presign response missing expected fields: %#v", body)
			}
		})
	}
}

func TestUploadPresignRejectsSVGAndOverFiveMiB(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		sizeBytes   int64
	}{
		{name: "svg", contentType: "image/svg+xml", sizeBytes: 1024},
		{name: "too large", contentType: "image/jpeg", sizeBytes: maxProductImageUploadBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := newAuthTestHandler(t)
			uploads := &fakeProductImageUploads{}
			handler.uploads = uploads

			request := authenticatedUploadRequest(t, handler, productImagePresignPath, map[string]any{
				"content_type": tc.contentType,
				"size_bytes":   tc.sizeBytes,
			}, validUploadCSRF)
			response, err := handler.Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
			}
			if uploads.presignCalls != 0 {
				t.Fatalf("presign calls = %d, want 0 for rejected request", uploads.presignCalls)
			}
			if strings.Contains(response.Body, "policy") {
				t.Fatalf("rejected response leaked policy: %q", response.Body)
			}
		})
	}
}

func TestProductImagePresignGeneratesConstrainedPolicyKeyAndShortExpiry(t *testing.T) {
	fakePresigner := &recordingPostPresigner{}
	service := productImageUploadService{
		bucketName: "product-images-bucket",
		keyPrefix:  "images",
		presigner:  fakePresigner,
	}
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

	response, err := service.Presign(context.Background(), productImagePresignRequest{ContentType: "image/webp", SizeBytes: 4096}, now)
	if err != nil {
		t.Fatalf("Presign returned error: %v", err)
	}
	if !strings.HasPrefix(response.Key, "images/products/uploads/2026/06/") || !strings.HasSuffix(response.Key, "/original.webp") {
		t.Fatalf("key = %q, want generated webp upload key under dated prefix", response.Key)
	}
	uploadID := strings.TrimSuffix(strings.TrimPrefix(response.Key, "images/products/uploads/2026/06/"), "/original.webp")
	if !looksLikeUUID(uploadID) {
		t.Fatalf("uploadID = %q, want UUID", uploadID)
	}
	if len(response.Variants) != len(productImageUploadVariantWidths)*2 {
		t.Fatalf("variants = %d, want %d", len(response.Variants), len(productImageUploadVariantWidths)*2)
	}
	if response.Variants[0].Key != "images/products/uploads/2026/06/"+uploadID+"/original-320w.webp" || response.Variants[0].Width != 320 || response.Variants[0].ContentType != "image/webp" {
		t.Fatalf("first variant = %#v, want first 320w webp sibling", response.Variants[0])
	}
	if response.ExpiresAt.Sub(now) != productImageUploadPresignTTL {
		t.Fatalf("expires_at = %s, want exactly %s after now", response.ExpiresAt, productImageUploadPresignTTL)
	}
	if fakePresigner.input == nil || aws.ToString(fakePresigner.input.Bucket) != "product-images-bucket" || aws.ToString(fakePresigner.input.Key) != response.Key || aws.ToString(fakePresigner.input.ContentType) != "image/webp" {
		t.Fatalf("presign input = %#v, want bucket/key/content-type", fakePresigner.input)
	}
	if fakePresigner.expires != productImageUploadPresignTTL {
		t.Fatalf("presign expires = %s, want %s", fakePresigner.expires, productImageUploadPresignTTL)
	}
	assertPolicyCondition(t, fakePresigner.conditions, []any{"content-length-range", float64(1), float64(maxProductImageUploadBytes)})
	assertPolicyCondition(t, fakePresigner.conditions, map[string]any{"Content-Type": "image/webp"})
}

func TestProductImagePresignPolicyIncludesExactKeyContentTypeAndSizeLimit(t *testing.T) {
	service := productImageUploadService{
		bucketName: "product-images-bucket",
		keyPrefix:  "images",
		presigner: s3.NewPresignClient(s3.NewFromConfig(aws.Config{
			Region:      "us-east-1",
			Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "SECRETEXAMPLE", ""),
		})),
	}

	response, err := service.Presign(context.Background(), productImagePresignRequest{ContentType: "image/jpeg", SizeBytes: maxProductImageUploadBytes}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Presign returned error: %v", err)
	}
	policy := decodedPresignPolicy(t, response.Fields["policy"])
	assertPolicyCondition(t, policy.Conditions, map[string]any{"key": response.Key})
	assertPolicyCondition(t, policy.Conditions, map[string]any{"Content-Type": "image/jpeg"})
	assertPolicyCondition(t, policy.Conditions, []any{"content-length-range", float64(1), float64(maxProductImageUploadBytes)})
	if response.Fields["Content-Type"] != "image/jpeg" {
		t.Fatalf("Content-Type field = %q, want image/jpeg", response.Fields["Content-Type"])
	}
	expiresAt, err := time.Parse(time.RFC3339, policy.Expiration)
	if err != nil {
		t.Fatalf("policy expiration parse: %v", err)
	}
	if remaining := time.Until(expiresAt); remaining < 4*time.Minute || remaining > 6*time.Minute {
		t.Fatalf("policy expiration remaining = %s, want around five minutes", remaining)
	}
}

func TestUploadConfirmVerifiesHeadObjectAndReturnsSiteRelativeURL(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	uploads := &fakeProductImageUploads{confirmURL: "/" + testUploadOriginalPNG}
	handler.uploads = uploads

	request := authenticatedUploadRequest(t, handler, productImageConfirmPath, map[string]any{
		"key":          testUploadOriginalPNG,
		"content_type": "image/png",
		"size_bytes":   2048,
	}, validUploadCSRF)
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusOK, response.Body)
	}
	if uploads.confirmCalls != 1 {
		t.Fatalf("confirm calls = %d, want 1", uploads.confirmCalls)
	}
	var body map[string]string
	decodeJSONResponse(t, response, &body)
	if body["url"] != "/"+testUploadOriginalPNG {
		t.Fatalf("url = %q, want site-relative verified upload URL", body["url"])
	}
	if strings.HasPrefix(body["url"], "http://") || strings.HasPrefix(body["url"], "https://") {
		t.Fatalf("url = %q, must not be external", body["url"])
	}
}

func TestProductImageConfirmVerifiesOriginalAndResponsiveVariants(t *testing.T) {
	service := productImageUploadService{
		bucketName:   "product-images-bucket",
		keyPrefix:    "images",
		objectClient: recordingImageSetObjectClient(testUploadOriginalJPG, "image/jpeg", testJPEGBytes),
	}

	url, err := service.Confirm(context.Background(), productImageConfirmRequest{
		Key:         testUploadOriginalJPG,
		ContentType: "image/jpeg",
		SizeBytes:   1024,
		Variants:    productImageConfirmVariantsForTest(testUploadOriginalJPG),
	})
	if err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}
	if url != "/"+testUploadOriginalJPG {
		t.Fatalf("Confirm URL = %q, want %q", url, "/"+testUploadOriginalJPG)
	}
}

func TestProductImageConfirmRequiresCompleteResponsiveVariants(t *testing.T) {
	valid := productImageConfirmVariantsForTest(testUploadOriginalJPG)
	tests := []struct {
		name     string
		variants []productImageConfirmVariant
	}{
		{name: "missing", variants: valid[:len(valid)-1]},
		{name: "duplicate", variants: append(append([]productImageConfirmVariant{}, valid[:len(valid)-1]...), valid[0])},
		{name: "wrong width", variants: func() []productImageConfirmVariant {
			got := append([]productImageConfirmVariant{}, valid...)
			got[0].Width = 321
			return got
		}()},
		{name: "wrong key", variants: func() []productImageConfirmVariant {
			got := append([]productImageConfirmVariant{}, valid...)
			got[0].Key = testUploadOriginalJPG
			return got
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateProductImageConfirmVariants(testUploadOriginalJPG, tc.variants); !errors.Is(err, errInvalidProductImageUpload) {
				t.Fatalf("validateProductImageConfirmVariants error = %v, want invalid upload", err)
			}
		})
	}
}

func TestProductImageConfirmRejectsInvalidResponsiveVariantObject(t *testing.T) {
	client := recordingImageSetObjectClient(testUploadOriginalJPG, "image/jpeg", testJPEGBytes)
	client.bodies[productImageVariantUploadKey(testUploadOriginalJPG, 320, "webp")] = []byte("<!doctype html>")
	service := productImageUploadService{
		bucketName:   "product-images-bucket",
		keyPrefix:    "images",
		objectClient: client,
	}

	_, err := service.Confirm(context.Background(), productImageConfirmRequest{
		Key:         testUploadOriginalJPG,
		ContentType: "image/jpeg",
		SizeBytes:   1024,
		Variants:    productImageConfirmVariantsForTest(testUploadOriginalJPG),
	})
	if !errors.Is(err, errInvalidProductImageUpload) {
		t.Fatalf("Confirm error = %v, want invalid upload", err)
	}
}

func TestProductImageConfirmRejectsMissingObjectAndMetadataMismatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		head    *s3.HeadObjectOutput
		headErr error
	}{
		{name: "nonexistent", headErr: errProductImageObjectNotFound},
		{name: "content type mismatch", head: &s3.HeadObjectOutput{ContentType: aws.String("image/svg+xml"), ContentLength: aws.Int64(1024)}},
		{name: "size mismatch", head: &s3.HeadObjectOutput{ContentType: aws.String("image/jpeg"), ContentLength: aws.Int64(1025)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := productImageUploadService{
				bucketName:   "product-images-bucket",
				keyPrefix:    "images",
				objectClient: &recordingObjectClient{output: tc.head, err: tc.headErr},
			}
			_, err := service.Confirm(context.Background(), productImageConfirmRequest{
				Key:         testUploadOriginalJPG,
				ContentType: "image/jpeg",
				SizeBytes:   1024,
				Variants:    productImageConfirmVariantsForTest(testUploadOriginalJPG),
			})
			if !errors.Is(err, errProductImageObjectNotFound) && !errors.Is(err, errInvalidProductImageUpload) {
				t.Fatalf("Confirm error = %v, want upload validation error", err)
			}
		})
	}
}

func TestProductImageConfirmValidatesMagicBytes(t *testing.T) {
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x48}
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R'}
	webpBytes := []byte{'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}

	tests := []struct {
		name        string
		contentType string
		extension   string
		body        []byte
		getErr      error
		wantErr     error
	}{
		{name: "valid jpeg", contentType: "image/jpeg", extension: "jpg", body: jpegBytes},
		{name: "valid png", contentType: "image/png", extension: "png", body: pngBytes},
		{name: "valid webp", contentType: "image/webp", extension: "webp", body: webpBytes},
		{name: "png bytes under jpeg content type", contentType: "image/jpeg", extension: "jpg", body: pngBytes, wantErr: errInvalidProductImageUpload},
		{name: "html payload", contentType: "image/png", extension: "png", body: []byte("<!doctype html><script>"), wantErr: errInvalidProductImageUpload},
		{name: "truncated body", contentType: "image/png", extension: "png", body: pngBytes[:4], wantErr: errInvalidProductImageUpload},
		{name: "object read failure", contentType: "image/jpeg", extension: "jpg", body: jpegBytes, getErr: errors.New("denied"), wantErr: errProductImageObjectNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &recordingObjectClient{
				output: &s3.HeadObjectOutput{
					ContentType:   aws.String(tc.contentType),
					ContentLength: aws.Int64(1024),
				},
				body:   tc.body,
				getErr: tc.getErr,
			}
			service := productImageUploadService{
				bucketName:   "product-images-bucket",
				keyPrefix:    "images",
				objectClient: client,
			}

			key := "images/products/uploads/2026/06/" + testUploadUUID + "/original." + tc.extension
			err := service.confirmUploadedImageObject(context.Background(), key, tc.contentType, 1024)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("confirmUploadedImageObject error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("confirmUploadedImageObject returned error: %v", err)
			}
			if got := aws.ToString(client.getInput.Range); got != "bytes=0-15" {
				t.Fatalf("GetObject range = %q, want bytes=0-15", got)
			}
		})
	}
}

func TestProductImageMagicBytesMatch(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		prefix      []byte
		want        bool
	}{
		{name: "jpeg minimal", contentType: "image/jpeg", prefix: []byte{0xFF, 0xD8, 0xFF}, want: true},
		{name: "jpeg short", contentType: "image/jpeg", prefix: []byte{0xFF, 0xD8}, want: false},
		{name: "png", contentType: "image/png", prefix: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, want: true},
		{name: "png wrong terminator", contentType: "image/png", prefix: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0B}, want: false},
		{name: "webp", contentType: "image/webp", prefix: []byte("RIFF\x10\x00\x00\x00WEBP"), want: true},
		{name: "riff but not webp", contentType: "image/webp", prefix: []byte("RIFF\x10\x00\x00\x00WAVE"), want: false},
		{name: "unknown content type", contentType: "image/gif", prefix: []byte("GIF89a"), want: false},
		{name: "empty prefix", contentType: "image/jpeg", prefix: nil, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := productImageMagicBytesMatch(tc.contentType, tc.prefix); got != tc.want {
				t.Fatalf("productImageMagicBytesMatch(%q) = %t, want %t", tc.contentType, got, tc.want)
			}
		})
	}
}

func TestProductImageConfirmRejectsCallerChosenTrustedPaths(t *testing.T) {
	service := productImageUploadService{
		bucketName: "product-images-bucket",
		keyPrefix:  "images",
		objectClient: &recordingObjectClient{output: &s3.HeadObjectOutput{
			ContentType:   aws.String("image/jpeg"),
			ContentLength: aws.Int64(1024),
		}},
	}

	for _, key := range []string{
		"https://evil.example/upload.jpg",
		"/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111.jpg",
		"images/products/../../secrets.jpg",
		"images/products/uploads/2026/06/not-a-uuid.jpg",
		"images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111.jpg",
		"images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/not-original.jpg",
		"images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.svg",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := service.Confirm(context.Background(), productImageConfirmRequest{Key: key, ContentType: "image/jpeg", SizeBytes: 1024})
			if !errors.Is(err, errInvalidProductImageUpload) {
				t.Fatalf("Confirm error = %v, want invalid upload", err)
			}
		})
	}
}

type fakeProductImageUploads struct {
	presignCalls    int
	confirmCalls    int
	presignResponse productImagePresignResponse
	confirmURL      string
}

func (f *fakeProductImageUploads) Presign(ctx context.Context, request productImagePresignRequest, now time.Time) (productImagePresignResponse, error) {
	_ = ctx
	_ = request
	_ = now
	f.presignCalls++
	return f.presignResponse, nil
}

func (f *fakeProductImageUploads) Confirm(ctx context.Context, request productImageConfirmRequest) (string, error) {
	_ = ctx
	_ = request
	f.confirmCalls++
	return f.confirmURL, nil
}

type recordingPostPresigner struct {
	input      *s3.PutObjectInput
	inputs     []*s3.PutObjectInput
	expires    time.Duration
	conditions []any
}

func (r *recordingPostPresigner) PresignPostObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error) {
	_ = ctx
	options := s3.PresignPostOptions{}
	for _, optFn := range optFns {
		optFn(&options)
	}
	if r.input == nil {
		r.input = input
		r.expires = options.Expires
		r.conditions = options.Conditions
	}
	r.inputs = append(r.inputs, input)
	return &s3.PresignedPostRequest{
		URL: "https://product-images-bucket.s3.us-east-1.amazonaws.com",
		Values: map[string]string{
			"policy": "recorded-policy",
		},
	}, nil
}

type recordingObjectClient struct {
	input    *s3.HeadObjectInput
	output   *s3.HeadObjectOutput
	outputs  map[string]*s3.HeadObjectOutput
	err      error
	getInput *s3.GetObjectInput
	body     []byte
	bodies   map[string][]byte
	getErr   error
}

func (r *recordingObjectClient) HeadObject(ctx context.Context, input *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	_ = ctx
	_ = optFns
	r.input = input
	if r.err != nil {
		return nil, r.err
	}
	if r.outputs != nil {
		output, ok := r.outputs[aws.ToString(input.Key)]
		if !ok {
			return nil, errProductImageObjectNotFound
		}
		return output, nil
	}
	return r.output, nil
}

func (r *recordingObjectClient) GetObject(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	_ = ctx
	_ = optFns
	r.getInput = input
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.bodies != nil {
		body, ok := r.bodies[aws.ToString(input.Key)]
		if !ok {
			return nil, errProductImageObjectNotFound
		}
		return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(r.body))}, nil
}

type presignPolicy struct {
	Expiration string `json:"expiration"`
	Conditions []any  `json:"conditions"`
}

const validUploadCSRF = "valid-upload-csrf"

func adminJSONRequest(method string, path string, payload map[string]any) events.APIGatewayV2HTTPRequest {
	request := adminRequest(method, path)
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	request.Headers = map[string]string{"Content-Type": "application/json"}
	request.Body = string(body)
	return request
}

func authenticatedUploadRequest(t *testing.T, handler *Handler, path string, payload map[string]any, csrfToken string) events.APIGatewayV2HTTPRequest {
	t.Helper()
	login := loginResponse(t, handler)
	sessionPair := cookiePair(t, responseCookie(t, login, adminSessionCookieName))
	csrfPair := cookiePair(t, responseCookie(t, login, adminCSRFCookieName))
	request := adminJSONRequest(http.MethodPost, path, payload)
	request.Cookies = []string{sessionPair, csrfPair}
	if csrfToken != "" {
		if csrfToken == validUploadCSRF {
			_, csrfToken, _ = strings.Cut(csrfPair, "=")
		}
		request.Headers[csrfHeaderName] = csrfToken
	}
	return request
}

func decodeJSONResponse(t *testing.T, response events.APIGatewayV2HTTPResponse, target any) {
	t.Helper()
	if response.Headers["Content-Type"] != httpapi.JSONContentType {
		t.Fatalf("Content-Type = %q, want %q", response.Headers["Content-Type"], httpapi.JSONContentType)
	}
	if err := json.Unmarshal([]byte(response.Body), target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body, err)
	}
}

func decodedPresignPolicy(t *testing.T, encoded string) presignPolicy {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	var policy presignPolicy
	if err := json.Unmarshal(decoded, &policy); err != nil {
		t.Fatalf("unmarshal policy %q: %v", string(decoded), err)
	}
	return policy
}

func assertPolicyCondition(t *testing.T, conditions []any, expected any) {
	t.Helper()
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatalf("marshal expected condition: %v", err)
	}
	for _, condition := range conditions {
		conditionJSON, err := json.Marshal(condition)
		if err != nil {
			t.Fatalf("marshal condition: %v", err)
		}
		if string(conditionJSON) == string(expectedJSON) {
			return
		}
	}
	t.Fatalf("conditions = %#v, missing %s", conditions, expectedJSON)
}

func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		switch i {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				return false
			}
		}
	}
	return true
}
