package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/admin"
	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
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

	handlers, err := newDevServerHandlers(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	mux := newDevServerMux(handlers, staticDir, imageDir)

	log.Printf("serving local SSR site at http://%s", address)
	if err := http.ListenAndServe(address, mux); err != nil {
		log.Fatal(err)
	}
}

func newDevServerMux(handlers devServerHandlers, staticDir string, imageDir string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(imageDir))))
	mux.HandleFunc("/__test/emails", handlers.handleFakeEmails)
	mux.HandleFunc("/__test/emails/clear", handlers.handleClearFakeEmails)
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin" || strings.HasPrefix(request.URL.Path, "/admin/") {
			handleAdmin(handlers.admin, responseWriter, request)
			return
		}
		handleSSR(handlers.ssr, responseWriter, request)
	})
	return mux
}

type devServerHandlers struct {
	ssr        *ssr.Handler
	admin      *admin.Handler
	fakeEmails *email.FakeSender
}

func newDevServerHandlers(ctx context.Context) (devServerHandlers, error) {
	fakeEmails := newDevServerFakeEmailSender()
	if os.Getenv(envDemoCatalogStore) == "1" {
		// One shared commerce store and fake payment provider back both
		// Lambdas, so orders placed on the storefront show up in the admin
		// order desk, and the shared catalog MemoryStore doubles as the
		// stock store for reservations and releases.
		store := catalog.NewMemoryStore(catalog.DemoCatalogProducts(), catalog.DemoCatalogCategories())
		commerceStore := commerce.NewMemoryStore()
		provider := payments.NewFakeProvider()
		credentials, err := admin.CredentialsFromEnvironment(ctx)
		if err != nil {
			return devServerHandlers{}, err
		}
		return devServerHandlers{
			ssr:        ssr.NewLocalDemoHandlerWithEmailSender(store, commerceStore, provider, fakeEmails),
			admin:      admin.NewLocalDemoHandlerWithEmailSender(credentials, store, commerceStore, store, provider, fakeEmails),
			fakeEmails: fakeEmails,
		}, nil
	}

	ssrHandler, err := ssr.NewHandlerFromEnvironment(ctx)
	if err != nil {
		return devServerHandlers{}, err
	}
	adminHandler, err := admin.NewHandlerFromEnvironment(ctx)
	if err != nil {
		return devServerHandlers{}, err
	}
	return devServerHandlers{ssr: ssrHandler, admin: adminHandler, fakeEmails: fakeEmails}, nil
}

func newDevServerFakeEmailSender() *email.FakeSender {
	if !fakeEmailCaptureEnvironmentEnabled() {
		return nil
	}
	fakeEmails := email.NewFakeSender()
	email.UseFakeSenderForEnvironment(fakeEmails)
	return fakeEmails
}

func fakeEmailCaptureEnvironmentEnabled() bool {
	return !appenv.IsProduction() && strings.EqualFold(strings.TrimSpace(os.Getenv(email.EnvSenderMode)), email.SenderKindFake)
}

type fakeEmailCaptureMessage struct {
	ID        string    `json:"id"`
	To        string    `json:"to"`
	Subject   string    `json:"subject"`
	Text      string    `json:"text"`
	HTML      string    `json:"html"`
	Kind      string    `json:"kind"`
	EventKey  string    `json:"eventKey"`
	CreatedAt time.Time `json:"createdAt"`
}

func (handlers devServerHandlers) handleFakeEmails(responseWriter http.ResponseWriter, request *http.Request) {
	if !handlers.fakeEmailCaptureEnabled() {
		http.NotFound(responseWriter, request)
		return
	}
	if request.Method != http.MethodGet {
		responseWriter.Header().Set("Allow", http.MethodGet)
		http.Error(responseWriter, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	writeFakeEmailCaptureJSON(responseWriter, sanitizedFakeEmailCaptureMessages(handlers.fakeEmails.List()))
}

func (handlers devServerHandlers) handleClearFakeEmails(responseWriter http.ResponseWriter, request *http.Request) {
	if !handlers.fakeEmailCaptureEnabled() {
		http.NotFound(responseWriter, request)
		return
	}
	if request.Method != http.MethodPost {
		responseWriter.Header().Set("Allow", http.MethodPost)
		http.Error(responseWriter, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	if recipient := strings.TrimSpace(request.URL.Query().Get("to")); recipient != "" {
		handlers.fakeEmails.ClearRecipient(recipient)
	} else {
		handlers.fakeEmails.Clear()
	}
	responseWriter.WriteHeader(http.StatusNoContent)
}

func (handlers devServerHandlers) fakeEmailCaptureEnabled() bool {
	return handlers.fakeEmails != nil && fakeEmailCaptureEnvironmentEnabled()
}

func sanitizedFakeEmailCaptureMessages(messages []email.Message) []fakeEmailCaptureMessage {
	capturedMessages := make([]fakeEmailCaptureMessage, 0, len(messages))
	for _, message := range messages {
		capturedMessages = append(capturedMessages, fakeEmailCaptureMessage{
			ID:        message.ID,
			To:        message.To,
			Subject:   message.Subject,
			Text:      message.Text,
			HTML:      message.HTML,
			Kind:      message.Kind,
			EventKey:  message.EventKey,
			CreatedAt: message.CreatedAt,
		})
	}
	return capturedMessages
}

func writeFakeEmailCaptureJSON(responseWriter http.ResponseWriter, messages []fakeEmailCaptureMessage) {
	responseWriter.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(responseWriter).Encode(messages); err != nil {
		http.Error(responseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func newDevServerHandler(ctx context.Context) (*ssr.Handler, error) {
	if os.Getenv(envDemoCatalogStore) == "1" {
		store := catalog.NewMemoryStore(catalog.DemoCatalogProducts(), catalog.DemoCatalogCategories())
		return ssr.NewLocalDemoHandler(store, commerce.NewMemoryStore(), payments.NewFakeProvider()), nil
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

type lambdaHandleFunc func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)

func handleSSR(handler *ssr.Handler, responseWriter http.ResponseWriter, request *http.Request) {
	serveLambda(handler.Handle, responseWriter, request)
}

func handleAdmin(handler *admin.Handler, responseWriter http.ResponseWriter, request *http.Request) {
	serveLambda(handler.Handle, responseWriter, request)
}

// serveLambda adapts a plain HTTP request into the API Gateway v2 event the
// Lambda handlers consume and writes their response back.
func serveLambda(handle lambdaHandleFunc, responseWriter http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(responseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	apiResponse, err := handle(request.Context(), events.APIGatewayV2HTTPRequest{
		RawPath:               request.URL.Path,
		RawQueryString:        request.URL.RawQuery,
		Headers:               requestHeaders(request),
		Cookies:               requestCookies(request),
		Body:                  string(body),
		IsBase64Encoded:       false,
		QueryStringParameters: queryParameters(request),
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method:   request.Method,
				Path:     request.URL.Path,
				SourceIP: requestSourceIP(request),
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

func requestSourceIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
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
