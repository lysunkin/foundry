package ui

import (
	"io/fs"
	"regexp"
	"testing"
)

// externalRef matches assets the browser loads from another origin: any src
// attribute, or a <link href>, with an absolute or protocol-relative URL.
// Plain <a href> navigation links are allowed.
var externalRef = regexp.MustCompile(`(?i)(?:\bsrc|<link\b[^>]*\bhref)\s*=\s*["']?\s*(?:https?:)?//`)

// The UI must work without network access, so templates may only reference
// assets served by the registry itself (R4).
func TestTemplatesHaveNoExternalAssets(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no templates found")
	}
	for _, f := range files {
		b, err := fs.ReadFile(templateFS, f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := externalRef.FindIndex(b); loc != nil {
			t.Errorf("%s references an external asset: %q", f, b[loc[0]:min(loc[1]+40, len(b))])
		}
	}
}

func TestExternalRef(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{`<script src="https://unpkg.com/htmx.min.js">`, true},
		{`<link href='http://cdn.example/x.css'>`, true},
		{`<script src="//cdn.example/x.js">`, true},
		{`<script SRC = "HTTPS://cdn.example/x.js">`, true},
		{`<script src="/static/js/htmx.min.js">`, false},
		{`<a href="/ui/repo/x">`, false},
		{`<a href="{{.URL}}">`, false},
		{`<a href="https://github.com/foundry/registry">`, false},
		{`<link rel="stylesheet" href="/static/css/main.css">`, false},
	} {
		if got := externalRef.MatchString(tc.in); got != tc.want {
			t.Errorf("externalRef(%q) = %t, want %t", tc.in, got, tc.want)
		}
	}
}
