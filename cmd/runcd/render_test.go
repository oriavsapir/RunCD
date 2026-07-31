package main

import "testing"

func TestShortDigest(t *testing.T) {
	cases := map[string]string{
		"":                                "-",
		"sha256:abcdef0123456789":         "sha256:abcdef01",
		"no-colon-at-all-in-this-string!": "no-colon-at-",
	}
	for in, want := range cases {
		if got := shortDigest(in); got != want {
			t.Errorf("shortDigest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(\"\") = %q, want \"-\"", got)
	}
	if got := orDash("value"); got != "value" {
		t.Errorf("orDash(\"value\") = %q, want \"value\"", got)
	}
}
