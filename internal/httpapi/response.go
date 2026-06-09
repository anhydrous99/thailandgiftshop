package httpapi

import (
	"maps"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
)

const HTMLContentType = "text/html; charset=utf-8"
const TextContentType = "text/plain; charset=utf-8"
const XMLContentType = "application/xml; charset=utf-8"
const JSONContentType = "application/json; charset=utf-8"

func HTMLResponse(statusCode int, body string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	return HTMLResponseWithCookies(statusCode, body, extraHeaders, nil)
}

func HTMLResponseWithCookies(statusCode int, body string, extraHeaders map[string]string, cookies []string) events.APIGatewayV2HTTPResponse {
	response := TypedResponse(statusCode, body, HTMLContentType, extraHeaders)
	response.Cookies = cookies
	return response
}

func TextResponse(statusCode int, body string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	return TypedResponse(statusCode, body, TextContentType, extraHeaders)
}

func XMLResponse(statusCode int, body string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	return TypedResponse(statusCode, body, XMLContentType, extraHeaders)
}

func TypedResponse(statusCode int, body string, contentType string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	headers := map[string]string{
		"Content-Type": contentType,
	}
	maps.Copy(headers, extraHeaders)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}
}

// PermanentRedirect issues a 308 keeping the request method intact.
func PermanentRedirect(location string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	headers := map[string]string{
		"Content-Type": HTMLContentType,
		"Location":     location,
	}
	maps.Copy(headers, extraHeaders)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusPermanentRedirect,
		Headers:    headers,
	}
}

// SeeOther issues a 303 so a mutating POST lands on a GET-able page.
func SeeOther(location string, cookies []string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	headers := map[string]string{
		"Content-Type": HTMLContentType,
		"Location":     location,
	}
	maps.Copy(headers, extraHeaders)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusSeeOther,
		Headers:    headers,
		Cookies:    cookies,
	}
}
