package main

import (
	"bytes"
	"testing"
)

func TestWriteVersion(t *testing.T) {
	previous := version
	version = "0.1.0-test"
	t.Cleanup(func() { version = previous })

	var out bytes.Buffer
	if err := writeVersion(&out); err != nil {
		t.Fatalf("writeVersion failed: %v", err)
	}
	if got := out.String(); got != "0.1.0-test\n" {
		t.Fatalf("version output = %q", got)
	}
}

func TestJoinedQueryPreservesLauncherArguments(t *testing.T) {
	if got := joinedQuery("hello", []string{"world", "photos"}); got != "hello world photos" {
		t.Fatalf("joined query = %q", got)
	}
	if got := joinedQuery("", []string{"hello", "world"}); got != "hello world" {
		t.Fatalf("positional query = %q", got)
	}
}
