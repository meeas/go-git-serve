package main

import (
	"errors"
	"strings"
	"testing"
)

func lines(ls ...string) func() (string, error) {
	return func() (string, error) {
		if len(ls) == 0 {
			return "", errors.New("no more input")
		}
		l := ls[0]
		ls = ls[1:]
		return l, nil
	}
}

func TestReadKeyLines(t *testing.T) {
	got, err := readKeyLines(lines("", "  -----BEGIN OPENSSH PRIVATE KEY-----", "abc", "def  ", "-----END OPENSSH PRIVATE KEY-----", "ignored"))
	want := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\ndef\n-----END OPENSSH PRIVATE KEY-----\n"
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := readKeyLines(lines("-----BEGIN RSA PRIVATE KEY-----", "abc", "")); err == nil || !strings.Contains(err.Error(), "ended early") {
		t.Fatalf("truncated paste: %v", err)
	}
	if got, _ := readKeyLines(lines("-----BEGIN RSA PRIVATE KEY-----", "x", "-----END RSA PRIVATE KEY-----")); !strings.HasSuffix(got, "-----END RSA PRIVATE KEY-----\n") {
		t.Fatalf("RSA key: %q", got)
	}
}

func TestSkipEscape(t *testing.T) {
	for in, want := range map[string]int{
		"\x1b[D":   3,  // left arrow
		"\x1b[3~x": 4,  // delete key
		"\x1bOH":   3,  // Home (SS3)
		"\x1b[":    -1, // incomplete
		"\x1b":     -1,
		"\x1bx":    1, // lone ESC
	} {
		if got := skipEscape([]byte(in)); got != want {
			t.Errorf("skipEscape(%q) = %d, want %d", in, got, want)
		}
	}
}
