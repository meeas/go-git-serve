// Package logx is a tiny levelled logger. Interactive runs print plain
// messages; daemon runs add timestamps so service logs are readable.
package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// Level controls which messages are printed.
type Level int

const (
	LevelQuiet   Level = iota // warnings and errors only
	LevelNormal               // + informational messages
	LevelVerbose              // + debug messages
)

var (
	mu     sync.Mutex
	level  = LevelNormal
	logger = log.New(os.Stderr, "", 0)
)

// Setup configures the output, level and whether lines carry timestamps.
func Setup(w io.Writer, l Level, timestamps bool) {
	mu.Lock()
	defer mu.Unlock()
	flags := 0
	if timestamps {
		flags = log.LstdFlags
	}
	logger = log.New(w, "", flags)
	level = l
}

// Writer returns the current log destination (used for git progress output).
func Writer() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	return logger.Writer()
}

// Verbose reports whether debug messages are enabled.
func Verbose() bool {
	mu.Lock()
	defer mu.Unlock()
	return level >= LevelVerbose
}

// Quiet reports whether informational messages are suppressed.
func Quiet() bool {
	mu.Lock()
	defer mu.Unlock()
	return level <= LevelQuiet
}

func output(min Level, prefix, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if level < min {
		return
	}
	logger.Print(prefix + fmt.Sprintf(format, args...))
}

// Debugf prints only in verbose mode.
func Debugf(format string, args ...any) { output(LevelVerbose, "debug: ", format, args...) }

// Infof prints unless quiet.
func Infof(format string, args ...any) { output(LevelNormal, "", format, args...) }

// Warnf always prints.
func Warnf(format string, args ...any) { output(LevelQuiet, "warning: ", format, args...) }

// Errorf always prints.
func Errorf(format string, args ...any) { output(LevelQuiet, "error: ", format, args...) }
