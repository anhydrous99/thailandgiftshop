// Package httpapi holds the API Gateway v2 HTTP event helpers shared by the
// ssr and admin Lambda handlers: request parsing, response building, and the
// CloudFront origin-secret check.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// Method returns the request's HTTP method, defaulting to GET when the event
// carries none (as devserver-built and test events may).
func Method(request events.APIGatewayV2HTTPRequest) string {
	if request.RequestContext.HTTP.Method != "" {
		return request.RequestContext.HTTP.Method
	}

	return http.MethodGet
}

// Path returns the request path, preferring RawPath and defaulting to "/".
func Path(request events.APIGatewayV2HTTPRequest) string {
	if request.RawPath != "" {
		return request.RawPath
	}
	if request.RequestContext.HTTP.Path != "" {
		return request.RequestContext.HTTP.Path
	}

	return "/"
}

// HeaderValue returns the value of the first header matching name
// case-insensitively, or "" when absent.
func HeaderValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}

	return ""
}

// CookieValue finds a cookie by name in the event's Cookies slice, falling
// back to a raw Cookie header for requests adapted from plain HTTP.
func CookieValue(request events.APIGatewayV2HTTPRequest, name string) (string, bool) {
	for _, cookieHeader := range request.Cookies {
		if value, found := NamedCookieValue(cookieHeader, name); found {
			return value, true
		}
	}
	if cookieHeader := HeaderValue(request.Headers, "Cookie"); cookieHeader != "" {
		return NamedCookieValue(cookieHeader, name)
	}

	return "", false
}

// NamedCookieValue extracts a cookie by name from a Cookie header value.
func NamedCookieValue(cookieHeader string, name string) (string, bool) {
	for part := range strings.SplitSeq(cookieHeader, ";") {
		cookieName, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && cookieName == name {
			return value, true
		}
	}

	return "", false
}

// FormValues parses a URL-encoded form body, transparently decoding base64
// bodies first.
func FormValues(request events.APIGatewayV2HTTPRequest) (url.Values, error) {
	body, err := bodyString(request)
	if err != nil {
		return nil, err
	}

	return url.ParseQuery(body)
}

// JSONBody decodes the request body as JSON into target, rejecting unknown
// fields.
func JSONBody(request events.APIGatewayV2HTTPRequest, target any) error {
	body, err := bodyString(request)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func bodyString(request events.APIGatewayV2HTTPRequest) (string, error) {
	if !request.IsBase64Encoded {
		return request.Body, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(request.Body)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
