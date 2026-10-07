package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := dispatch([]string{"version"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "mind-runner ") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := dispatch([]string{"nope"}, &out, &errb)
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Fatalf("stderr=%q", errb.String())
	}
}
