package ssr

import (
	"bytes"
	"context"
)

// supportEmail is the single customer-facing contact address surfaced in the
// footer, the contact page, and the policy pages.
const supportEmail = "support@thailandgiftshop.com"

// renderPolicyPage renders one of the shared static content pages (shipping,
// returns, privacy, terms, contact).
func (h *Handler) renderPolicyPage(ctx context.Context, kind pageKind, headerCartLabel string) (string, error) {
	vm := policyViewModelForKind(kind)
	vm.HeaderCartLabel = headerCartLabel
	var body bytes.Buffer
	if err := policyPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func policyViewModelForKind(kind pageKind) policyPageViewModel {
	switch kind {
	case pageReturns:
		return returnsPolicyViewModel()
	case pagePrivacy:
		return privacyPolicyViewModel()
	case pageTerms:
		return termsPolicyViewModel()
	case pageContact:
		return contactPageViewModel()
	default:
		return shippingPolicyViewModel()
	}
}

func shippingPolicyViewModel() policyPageViewModel {
	return policyPageViewModel{
		Metadata:    shippingMetadata(),
		Breadcrumbs: shippingBreadcrumbs(),
		Eyebrow:     "Help",
		Title:       "Shipping",
		Intro:       "Thailand Gift Shop ships within the United States only.",
		Sections: []policySection{
			{Heading: "Where we ship", Paragraphs: []string{"We currently ship to shipping addresses in the United States only. We are not able to ship internationally yet."}},
			{Heading: "Shipping cost", Paragraphs: []string{"Shipping is free on every order while we launch. Any future shipping charge will be shown clearly at checkout before you pay."}},
			{Heading: "Processing and delivery", Paragraphs: []string{"Orders are prepared after your payment is confirmed by Stripe. You will receive an email when your order is placed and another when it ships. Sales tax is not collected yet."}},
			{Heading: "Tracking", Paragraphs: []string{"When your order ships, the tracking details appear on your order page and are emailed to you. Signed-in shoppers can review every order under Orders; guests get a private order link in their confirmation email."}},
		},
		UpdatedNote: "Questions about a shipment? Contact us and include your order number.",
	}
}

func returnsPolicyViewModel() policyPageViewModel {
	return policyPageViewModel{
		Metadata:    returnsMetadata(),
		Breadcrumbs: returnsBreadcrumbs(),
		Eyebrow:     "Help",
		Title:       "Returns and refunds",
		Intro:       "If something is not right with your order, we want to make it right.",
		Sections: []policySection{
			{Heading: "Requesting a return", Paragraphs: []string{"Contact us within 30 days of delivery with your order number and what went wrong, and we will reply with the next steps."}},
			{Heading: "Refunds", Paragraphs: []string{"Approved refunds are returned in full to your original payment method through Stripe, and you will receive an order email when the refund is issued. How quickly the refund appears after that depends on your bank or card issuer."}},
			{Heading: "Damaged or incorrect items", Paragraphs: []string{"If an item arrives damaged, or you received the wrong item, contact us right away and we will arrange a replacement or refund."}},
		},
		UpdatedNote: "To start a return or refund, contact us with your order number.",
	}
}

func privacyPolicyViewModel() policyPageViewModel {
	return policyPageViewModel{
		Metadata:    privacyMetadata(),
		Breadcrumbs: privacyBreadcrumbs(),
		Eyebrow:     "Legal",
		Title:       "Privacy Policy",
		Intro:       "This explains how Thailand Gift Shop handles your personal information.",
		Draft:       true,
		Sections: []policySection{
			{Heading: "Information we collect", Paragraphs: []string{"When you create an account or place an order, we collect your email address, shipping address, and order history. We never see or store your card number — card payments are handled entirely by Stripe on its hosted checkout page."}},
			{Heading: "How we use your information", Paragraphs: []string{"We use your information to process and ship your orders, send order and account emails, and provide customer support."}},
			{Heading: "Cookies", Paragraphs: []string{"We use only cookies that are necessary for the site to work, such as your cart and your signed-in session. We do not use advertising or tracking cookies."}},
			{Heading: "Payments", Paragraphs: []string{"Payments are processed by Stripe. Stripe's handling of your payment information is governed by Stripe's own privacy policy."}},
			{Heading: "Contacting us", Paragraphs: []string{"For any privacy question, or to request access to or deletion of your information, contact us."}},
		},
		UpdatedNote: "This draft will be replaced with a reviewed Privacy Policy before launch.",
	}
}

func termsPolicyViewModel() policyPageViewModel {
	return policyPageViewModel{
		Metadata:    termsMetadata(),
		Breadcrumbs: termsBreadcrumbs(),
		Eyebrow:     "Legal",
		Title:       "Terms of Service",
		Intro:       "These terms govern your use of Thailand Gift Shop.",
		Draft:       true,
		Sections: []policySection{
			{Heading: "Using this site", Paragraphs: []string{"By browsing and ordering from Thailand Gift Shop, you agree to these terms. Please do not use the site if you do not agree."}},
			{Heading: "Orders and pricing", Paragraphs: []string{"All prices are in US dollars. We may correct pricing or availability errors and cancel an affected order with a full refund."}},
			{Heading: "Payments", Paragraphs: []string{"Payments are processed securely by Stripe. By placing an order, you authorize the charge shown at checkout."}},
			{Heading: "Contact", Paragraphs: []string{"Questions about these terms? Contact us."}},
		},
		UpdatedNote: "This draft will be replaced with reviewed Terms of Service before launch.",
	}
}

func contactPageViewModel() policyPageViewModel {
	return policyPageViewModel{
		Metadata:     contactMetadata(),
		Breadcrumbs:  contactBreadcrumbs(),
		Eyebrow:      "Help",
		Title:        "Contact us",
		Intro:        "We are happy to help with orders, shipping, returns, and product questions.",
		ContactEmail: supportEmail,
		Sections: []policySection{
			{Heading: "Email", Paragraphs: []string{"Email is the best way to reach us. Include your order number if your question is about an existing order so we can help faster."}},
			{Heading: "Response time", Paragraphs: []string{"We aim to reply within two business days."}},
		},
	}
}
