//go:build !windows

package main

// enableVTOutput is a no-op: Unix terminals understand escape sequences.
func enableVTOutput() {}
