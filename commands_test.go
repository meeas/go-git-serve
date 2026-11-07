package main

import (
	"slices"
	"testing"
)

func TestIgnoredFlags(t *testing.T) {
	f := newFlags("app")
	if err := f.parse([]string{"--config", "c.toml", "-v", "--interval", "5m", "--repo", "x", "--randomize-start=false", "--log-file", "l"}); err != nil {
		t.Fatal(err)
	}
	got := ignoredFlags(f, "config", "log-file", "verbose", "quiet")
	want := []string{"--interval", "--repo", "--randomize-start"} // order typed
	if !slices.Equal(got, want) {
		t.Errorf("ignoredFlags = %v, want %v", got, want)
	}
	if got := ignoredFlags(newFlags("app"), "config"); len(got) != 0 {
		t.Errorf("no flags given: %v", got)
	}
}

func TestIgnoredEnv(t *testing.T) {
	set := map[string]string{"GGS_UPDATE_INTERVAL": "5m", "GGS_AUTH_TOKEN": "secret", "HOME": "/x"}
	got := ignoredEnv(func(k string) (string, bool) { v, ok := set[k]; return v, ok })
	if !slices.Contains(got, "GGS_UPDATE_INTERVAL") || !slices.Contains(got, "GGS_AUTH_TOKEN") || len(got) != 2 {
		t.Errorf("ignoredEnv = %v", got)
	}
	if slices.Contains(got, "secret") {
		t.Error("a secret value was listed")
	}
}
