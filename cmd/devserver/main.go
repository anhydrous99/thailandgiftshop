package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/ssr"
	"github.com/aws/aws-lambda-go/events"
)

const (
	defaultHost         = "127.0.0.1"
	defaultPort         = "8080"
	defaultStaticDir    = "web/static"
	defaultImageDir     = "web/product-images"
	envDemoCatalogStore = "CATALOG_DEMO_STORE"
)

func main() {
	host := envOrDefault("HOST", defaultHost)
	port := envOrDefault("PORT", defaultPort)
	staticDir := envOrDefault("STATIC_DIR", defaultStaticDir)
	imageDir := envOrDefault("IMAGE_DIR", defaultImageDir)
	address := net.JoinHostPort(host, port)

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(imageDir))))
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, request *http.Request) {
		handleSSR(handler, responseWriter, request)
	})

	log.Printf("serving local SSR site at http://%s", address)
	if err := http.ListenAndServe(address, mux); err != nil {
		log.Fatal(err)
	}
}

func newDevServerHandler(ctx context.Context) (*ssr.Handler, error) {
	if os.Getenv(envDemoCatalogStore) == "1" {
		return ssr.NewHandler(catalog.NewDemoStore()), nil
	}

	return ssr.NewHandlerFromEnvironment(ctx)
}

func envOrDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}

	return value
}

func handleSSR(handler *ssr.Handler, responseWriter http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(responseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	apiResponse, err := handler.Handle(request.Context(), events.APIGatewayV2HTTPRequest{
		RawPath:               request.URL.Path,
		RawQueryString:        request.URL.RawQuery,
		Headers:               requestHeaders(request),
		Cookies:               requestCookies(request),
		Body:                  string(body),
		IsBase64Encoded:       false,
		QueryStringParameters: queryParameters(request),
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: request.Method,
				Path:   request.URL.Path,
			},
		},
	})
	if err != nil {
		http.Error(responseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	for key, value := range apiResponse.Headers {
		responseWriter.Header().Set(key, value)
	}
	for _, cookie := range apiResponse.Cookies {
		responseWriter.Header().Add("Set-Cookie", cookie)
	}
	responseWriter.WriteHeader(apiResponse.StatusCode)
	if request.Method != http.MethodHead {
		_, _ = responseWriter.Write([]byte(apiResponse.Body))
	}
}

func requestHeaders(request *http.Request) map[string]string {
	headers := make(map[string]string, len(request.Header))
	for key, values := range request.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	if hxRequest := request.Header.Get("HX-Request"); hxRequest != "" {
		headers["HX-Request"] = hxRequest
	}
	if contentType := request.Header.Get("Content-Type"); contentType != "" {
		headers["content-type"] = contentType
	}
	return headers
}

func requestCookies(request *http.Request) []string {
	cookies := request.Cookies()
	values := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		values = append(values, cookie.Name+"="+cookie.Value)
	}
	return values
}

func queryParameters(request *http.Request) map[string]string {
	parameters := make(map[string]string, len(request.URL.Query()))
	for key, values := range request.URL.Query() {
		if len(values) > 0 {
			parameters[key] = values[0]
		}
	}
	return parameters
}
