package main

import "testing"

func TestJoinedQueryPreservesLauncherArguments(t *testing.T) {
	if got := joinedQuery("hello", []string{"world", "photos"}); got != "hello world photos" {
		t.Fatalf("joined query = %q", got)
	}
	if got := joinedQuery("", []string{"hello", "world"}); got != "hello world" {
		t.Fatalf("positional query = %q", got)
	}
}
