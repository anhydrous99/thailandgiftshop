package ssr

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDefaultSiteHeadAvoidsInlineStylesAndGlobalScripts(t *testing.T) {
	var buffer bytes.Buffer
	if err := siteHead(homeMetadata()).Render(context.Background(), &buffer); err != nil {
		t.Fatalf("render site head: %v", err)
	}

	body := buffer.String()
	for _, unwanted := range []string{"<style", "/static/vendor/htmx.min.js", "/static/js/enhance.js"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("rendered head unexpectedly contains %q: %q", unwanted, body)
		}
	}
}
