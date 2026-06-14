package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const EnvProductImagesBucketName = "PRODUCT_IMAGES_BUCKET_NAME"
const EnvProductImagesKeyPrefix = "PRODUCT_IMAGES_KEY_PREFIX"

const productImagePresignPath = "/admin/uploads/product-image/presign"
const productImageConfirmPath = "/admin/uploads/product-image/confirm"

const defaultProductImagesKeyPrefix = "images"
const productImageUploadPresignTTL = 5 * time.Minute
const maxProductImageUploadBytes int64 = 5 * 1024 * 1024

var productImageUploadVariantWidths = []int{320, 480, 720, 960, 1200}

var allowedProductImageTypes = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
}

var extensionProductImageTypes = map[string]string{
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
}

var errInvalidProductImageUpload = errors.New("invalid product image upload")
var errProductImageObjectNotFound = errors.New("product image object not found")

type productImageUploads interface {
	Presign(context.Context, productImagePresignRequest, time.Time) (productImagePresignResponse, error)
	Confirm(context.Context, productImageConfirmRequest) (string, error)
}

type productImagePresignRequest struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type productImagePresignResponse struct {
	URL          string                       `json:"url"`
	Fields       map[string]string            `json:"fields"`
	Key          string                       `json:"key"`
	ExpiresAt    time.Time                    `json:"expires_at"`
	MaxSizeBytes int64                        `json:"max_size_bytes"`
	ContentType  string                       `json:"content_type"`
	Variants     []productImagePresignVariant `json:"variants,omitempty"`
}

type productImagePresignVariant struct {
	URL         string            `json:"url"`
	Fields      map[string]string `json:"fields"`
	Key         string            `json:"key"`
	Width       int               `json:"width"`
	ContentType string            `json:"content_type"`
}

type productImageConfirmRequest struct {
	Key         string                       `json:"key"`
	ContentType string                       `json:"content_type"`
	SizeBytes   int64                        `json:"size_bytes"`
	Variants    []productImageConfirmVariant `json:"variants"`
}

type productImageConfirmVariant struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Width       int    `json:"width"`
}

type s3PostPresigner interface {
	PresignPostObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error)
}

type s3ObjectClient interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type productImageUploadService struct {
	bucketName   string
	keyPrefix    string
	presigner    s3PostPresigner
	objectClient s3ObjectClient
}

func productImageUploadServiceFromEnvironment(ctx context.Context) (*productImageUploadService, error) {
	bucketName := strings.TrimSpace(os.Getenv(EnvProductImagesBucketName))
	if bucketName == "" {
		return nil, errInvalidProductImageUpload
	}
	keyPrefix := normalizedProductImageKeyPrefix(os.Getenv(EnvProductImagesKeyPrefix))

	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsConfig)
	return &productImageUploadService{
		bucketName:   bucketName,
		keyPrefix:    keyPrefix,
		presigner:    s3.NewPresignClient(client),
		objectClient: client,
	}, nil
}

func (s productImageUploadService) Presign(ctx context.Context, request productImagePresignRequest, now time.Time) (productImagePresignResponse, error) {
	contentType, extension, ok := allowedProductImageType(request.ContentType)
	if !ok || !validUploadSize(request.SizeBytes) || s.bucketName == "" || s.presigner == nil {
		return productImagePresignResponse{}, errInvalidProductImageUpload
	}

	key, err := generatedProductImageUploadKey(normalizedProductImageKeyPrefix(s.keyPrefix), extension, now)
	if err != nil {
		return productImagePresignResponse{}, err
	}
	post, err := s.presignPost(ctx, key, contentType)
	if err != nil {
		return productImagePresignResponse{}, err
	}
	variants, err := s.presignVariants(ctx, key)
	if err != nil {
		return productImagePresignResponse{}, err
	}
	fields := map[string]string{"Content-Type": contentType}
	for name, value := range post.Values {
		fields[name] = value
	}
	return productImagePresignResponse{
		URL:          post.URL,
		Fields:       fields,
		Key:          key,
		ExpiresAt:    now.UTC().Add(productImageUploadPresignTTL),
		MaxSizeBytes: maxProductImageUploadBytes,
		ContentType:  contentType,
		Variants:     variants,
	}, nil
}

func (s productImageUploadService) presignPost(ctx context.Context, key string, contentType string) (*s3.PresignedPostRequest, error) {
	return s.presigner.PresignPostObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucketName),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, func(options *s3.PresignPostOptions) {
		options.Expires = productImageUploadPresignTTL
		options.Conditions = []any{
			map[string]string{"Content-Type": contentType},
			[]any{"content-length-range", 1, maxProductImageUploadBytes},
		}
	})
}

func (s productImageUploadService) presignVariants(ctx context.Context, originalKey string) ([]productImagePresignVariant, error) {
	variants := make([]productImagePresignVariant, 0, len(productImageUploadVariantWidths)*2)
	for _, width := range productImageUploadVariantWidths {
		for _, format := range []struct {
			contentType string
			extension   string
		}{
			{contentType: "image/webp", extension: "webp"},
			{contentType: "image/jpeg", extension: "jpg"},
		} {
			key := productImageVariantUploadKey(originalKey, width, format.extension)
			post, err := s.presignPost(ctx, key, format.contentType)
			if err != nil {
				return nil, err
			}
			fields := map[string]string{"Content-Type": format.contentType}
			for name, value := range post.Values {
				fields[name] = value
			}
			variants = append(variants, productImagePresignVariant{URL: post.URL, Fields: fields, Key: key, Width: width, ContentType: format.contentType})
		}
	}
	return variants, nil
}

func (s productImageUploadService) Confirm(ctx context.Context, request productImageConfirmRequest) (string, error) {
	contentType, extension, ok := allowedProductImageType(request.ContentType)
	if !ok || !validUploadSize(request.SizeBytes) || s.bucketName == "" || s.objectClient == nil {
		return "", errInvalidProductImageUpload
	}
	keyPrefix := normalizedProductImageKeyPrefix(s.keyPrefix)
	if err := validateProductImageUploadKey(keyPrefix, request.Key, extension); err != nil {
		return "", err
	}
	if err := validateProductImageConfirmVariants(request.Key, request.Variants); err != nil {
		return "", err
	}
	if err := s.confirmUploadedImageObject(ctx, request.Key, contentType, request.SizeBytes); err != nil {
		return "", err
	}
	for _, variant := range request.Variants {
		variantContentType, _, _ := allowedProductImageType(variant.ContentType)
		if err := s.confirmUploadedImageObject(ctx, variant.Key, variantContentType, variant.SizeBytes); err != nil {
			return "", err
		}
	}

	return "/" + request.Key, nil
}

func (s productImageUploadService) confirmUploadedImageObject(ctx context.Context, key string, contentType string, sizeBytes int64) error {
	head, err := s.objectClient.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return errProductImageObjectNotFound
	}
	if head == nil || strings.TrimSpace(aws.ToString(head.ContentType)) != contentType || aws.ToInt64(head.ContentLength) != sizeBytes {
		return errInvalidProductImageUpload
	}
	return s.validateUploadedImageBytes(ctx, key, contentType)
}

const productImageMagicByteProbeLength = 16

// validateUploadedImageBytes reads the first bytes of the uploaded object and
// checks the file signature, so a non-image payload uploaded with an image
// Content-Type never becomes a product image URL.
func (s productImageUploadService) validateUploadedImageBytes(ctx context.Context, key string, contentType string) error {
	object, err := s.objectClient.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", productImageMagicByteProbeLength-1)),
	})
	if err != nil {
		return errProductImageObjectNotFound
	}
	if object == nil || object.Body == nil {
		return errInvalidProductImageUpload
	}
	defer object.Body.Close()
	prefix, err := io.ReadAll(io.LimitReader(object.Body, productImageMagicByteProbeLength))
	if err != nil || !productImageMagicBytesMatch(contentType, prefix) {
		return errInvalidProductImageUpload
	}
	return nil
}

func productImageMagicBytesMatch(contentType string, prefix []byte) bool {
	switch contentType {
	case "image/jpeg":
		return len(prefix) >= 3 && bytes.Equal(prefix[:3], []byte{0xFF, 0xD8, 0xFF})
	case "image/png":
		return len(prefix) >= 8 && bytes.Equal(prefix[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	case "image/webp":
		return len(prefix) >= 12 && bytes.Equal(prefix[0:4], []byte("RIFF")) && bytes.Equal(prefix[8:12], []byte("WEBP"))
	}
	return false
}

func isProductImageUploadPath(path string) bool {
	return path == productImagePresignPath || path == productImageConfirmPath
}

func isInvalidProductImageUpload(err error) bool {
	return errors.Is(err, errInvalidProductImageUpload)
}

func isProductImageObjectNotFound(err error) bool {
	return errors.Is(err, errProductImageObjectNotFound)
}

func validateProductImagePresignRequest(request productImagePresignRequest) error {
	if _, _, ok := allowedProductImageType(request.ContentType); !ok || !validUploadSize(request.SizeBytes) {
		return errInvalidProductImageUpload
	}
	return nil
}

func allowedProductImageType(value string) (string, string, bool) {
	contentType := strings.TrimSpace(strings.ToLower(value))
	extension, ok := allowedProductImageTypes[contentType]
	return contentType, extension, ok
}

func validUploadSize(sizeBytes int64) bool {
	return sizeBytes > 0 && sizeBytes <= maxProductImageUploadBytes
}

func generatedProductImageUploadKey(keyPrefix string, extension string, now time.Time) (string, error) {
	uuid, err := randomUUID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/products/uploads/%04d/%02d/%s/original.%s", keyPrefix, now.UTC().Year(), int(now.UTC().Month()), uuid, extension), nil
}

func validateProductImageUploadKey(keyPrefix string, key string, expectedExtension string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.Contains(key, `\`) || strings.Contains(key, "://") {
		return errInvalidProductImageUpload
	}
	prefix := keyPrefix + "/products/uploads/"
	if !strings.HasPrefix(key, prefix) {
		return errInvalidProductImageUpload
	}
	parts := strings.Split(strings.TrimPrefix(key, prefix), "/")
	if len(parts) != 4 || !validYearMonth(parts[0], parts[1]) || !looksLikeUploadUUID(parts[2]) {
		return errInvalidProductImageUpload
	}
	filename, extension, ok := strings.Cut(parts[3], ".")
	if !ok || filename != "original" || strings.Contains(extension, ".") {
		return errInvalidProductImageUpload
	}
	contentType, ok := extensionProductImageTypes[extension]
	if !ok || allowedProductImageTypes[contentType] != expectedExtension || extension != expectedExtension {
		return errInvalidProductImageUpload
	}
	return nil
}

func validateProductImageConfirmVariants(originalKey string, variants []productImageConfirmVariant) error {
	if len(variants) != len(productImageUploadVariantWidths)*2 {
		return errInvalidProductImageUpload
	}
	seen := map[string]bool{}
	for _, variant := range variants {
		_, extension, ok := allowedProductImageType(variant.ContentType)
		if !ok || !validUploadSize(variant.SizeBytes) || !validProductImageVariantWidth(variant.Width) {
			return errInvalidProductImageUpload
		}
		expectedKey := productImageVariantUploadKey(originalKey, variant.Width, extension)
		if variant.Key != expectedKey || seen[variant.Key] {
			return errInvalidProductImageUpload
		}
		seen[variant.Key] = true
	}
	return nil
}

func productImageVariantUploadKey(originalKey string, width int, extension string) string {
	return strings.TrimSuffix(originalKey, path.Ext(originalKey)) + fmt.Sprintf("-%dw.%s", width, extension)
}

func validProductImageVariantWidth(width int) bool {
	for _, candidate := range productImageUploadVariantWidths {
		if width == candidate {
			return true
		}
	}
	return false
}

func validYearMonth(year string, month string) bool {
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

func normalizedProductImageKeyPrefix(value string) string {
	prefix := strings.Trim(strings.TrimSpace(value), "/")
	if prefix == "" {
		return defaultProductImagesKeyPrefix
	}
	return prefix
}

func randomUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func looksLikeUploadUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
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
