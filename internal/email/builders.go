package email

import (
	"fmt"
	"html"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
)

func BuildPasswordReset(to string, resetLink string) Message {
	text := strings.Join([]string{
		"Reset your Thailand Gift Shop password using this link:",
		resetLink,
		"This link expires in 30 minutes.",
	}, "\n\n")
	htmlBody := "<p>Reset your Thailand Gift Shop password using this link:</p>" +
		fmt.Sprintf("<p><a href=%q>Reset your password</a></p>", html.EscapeString(resetLink)) +
		"<p>This link expires in 30 minutes.</p>"

	return Message{
		To:       strings.TrimSpace(to),
		Subject:  "Reset your Thailand Gift Shop password",
		Text:     text,
		HTML:     htmlBody,
		Kind:     MessageKindPasswordReset,
		EventKey: "password_reset:" + strings.ToLower(strings.TrimSpace(to)),
	}
}

func BuildOrderPlaced(order commerce.Order, orderURL string) Message {
	status := string(order.Status)
	text := strings.Join([]string{
		fmt.Sprintf("Order %s was placed.", order.ID),
		fmt.Sprintf("Status: %s", status),
		fmt.Sprintf("View order: %s", orderURL),
	}, "\n")
	htmlBody := strings.Join([]string{
		fmt.Sprintf("<p>Order %s was placed.</p>", html.EscapeString(order.ID)),
		fmt.Sprintf("<p>Status: %s</p>", html.EscapeString(status)),
		orderLinkHTML(orderURL),
	}, "")

	return orderMessage(order, MessageKindOrderPlaced, "order:"+order.ID+":placed", fmt.Sprintf("Order %s was placed", order.ID), text, htmlBody)
}

func BuildOrderStatusChange(order commerce.Order, from commerce.OrderStatus, to commerce.OrderStatus, orderURL string) Message {
	fromStatus := string(from)
	toStatus := string(to)
	text := strings.Join([]string{
		fmt.Sprintf("Order %s status changed from %s to %s.", order.ID, fromStatus, toStatus),
		fmt.Sprintf("Current status: %s", string(order.Status)),
		trackingLine(order),
		fmt.Sprintf("View order: %s", orderURL),
	}, "\n")
	htmlBody := strings.Join([]string{
		fmt.Sprintf("<p>Order %s status changed from %s to %s.</p>", html.EscapeString(order.ID), html.EscapeString(fromStatus), html.EscapeString(toStatus)),
		fmt.Sprintf("<p>Current status: %s</p>", html.EscapeString(string(order.Status))),
		trackingLineHTML(order),
		orderLinkHTML(orderURL),
	}, "")

	return orderMessage(order, MessageKindOrderStatusChange, "order:"+order.ID+":status:"+fromStatus+":"+toStatus, fmt.Sprintf("Order %s status: %s", order.ID, toStatus), text, htmlBody)
}

func BuildTrackingUpdate(order commerce.Order, orderURL string) Message {
	line := trackingLine(order)
	text := strings.Join([]string{
		fmt.Sprintf("Tracking update for order %s.", order.ID),
		fmt.Sprintf("Status: %s", string(order.Status)),
		line,
		fmt.Sprintf("View order: %s", orderURL),
	}, "\n")
	htmlBody := strings.Join([]string{
		fmt.Sprintf("<p>Tracking update for order %s.</p>", html.EscapeString(order.ID)),
		fmt.Sprintf("<p>Status: %s</p>", html.EscapeString(string(order.Status))),
		trackingLineHTML(order),
		orderLinkHTML(orderURL),
	}, "")

	return orderMessage(order, MessageKindTrackingUpdate, "order:"+order.ID+":tracking", fmt.Sprintf("Tracking update for order %s", order.ID), text, htmlBody)
}

func orderMessage(order commerce.Order, kind string, eventKey string, subject string, text string, htmlBody string) Message {
	return Message{
		To:       strings.TrimSpace(order.Email),
		Subject:  subject,
		Text:     text,
		HTML:     htmlBody,
		Kind:     kind,
		EventKey: eventKey,
	}
}

func trackingLine(order commerce.Order) string {
	carrier := strings.TrimSpace(order.TrackingCarrier)
	number := strings.TrimSpace(order.TrackingNumber)
	switch {
	case carrier != "" && number != "":
		return fmt.Sprintf("Tracking: %s %s", carrier, number)
	case carrier != "":
		return "Tracking carrier: " + carrier
	case number != "":
		return "Tracking number: " + number
	default:
		return "Tracking details are not available yet."
	}
}

func trackingLineHTML(order commerce.Order) string {
	return fmt.Sprintf("<p>%s</p>", html.EscapeString(trackingLine(order)))
}

func orderLinkHTML(orderURL string) string {
	return fmt.Sprintf("<p><a href=%q>View your order</a></p>", html.EscapeString(orderURL))
}
