package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
)

// Interactive prompts. On a terminal, lines are read with x/term's line
// editor (arrow keys, Home/End, Ctrl+U, backspace) and secrets are echoed
// as asterisks. Without a terminal, plain lines are read from stdin.

// errCancelled is returned when the user presses Ctrl+C or Ctrl+D at a
// prompt (raw mode turns Ctrl+C into an ordinary key press).
var errCancelled = &exitError{code: 130, err: errors.New("cancelled")}

var (
	stdinLines = bufio.NewReader(os.Stdin)
	vtOnce     sync.Once
)

func isTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// console joins stdin and stderr for x/term's Terminal.
type console struct{}

func (console) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (console) Write(p []byte) (int, error) { return os.Stderr.Write(p) }

// withRaw runs fn with the terminal in raw mode.
func withRaw(fn func() (string, error)) (string, error) {
	vtOnce.Do(enableVTOutput)
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	defer term.Restore(fd, old)
	return fn()
}

// readLine reads one edited line after prompt.
func readLine(prompt string) (string, error) {
	if !isTerminal() {
		fmt.Fprint(os.Stderr, prompt)
		line, err := stdinLines.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return "", fmt.Errorf("reading input: %w", err)
		}
		return strings.TrimSpace(line), nil
	}
	return withRaw(func() (string, error) {
		line, err := term.NewTerminal(console{}, prompt).ReadLine()
		if errors.Is(err, io.EOF) {
			return "", errCancelled
		}
		return strings.TrimSpace(line), err
	})
}

// ask prompts on the same line, returning def when the answer is empty.
func ask(label, def string) (string, error) {
	prompt := label + ": "
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", label, def)
	}
	v, err := readLine(prompt)
	if v == "" && err == nil {
		v = def
	}
	return v, err
}

// askLong is ask for long values (URLs, paths): the label goes on its own
// line and the answer is typed on a fresh line below it.
func askLong(label, def string) (string, error) {
	fmt.Fprintln(os.Stderr, label+":")
	if def != "" {
		fmt.Fprintf(os.Stderr, "  (press Enter to keep: %s)\n", def)
	}
	v, err := readLine("> ")
	if v == "" && err == nil {
		v = def
	}
	return v, err
}

// askUntil repeats a prompt until check accepts the answer.
func askUntil(prompt func(label, def string) (string, error), label, def string, check func(string) error) (string, error) {
	for {
		v, err := prompt(label, def)
		if err != nil {
			return "", err
		}
		if err := check(v); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			continue
		}
		return v, nil
	}
}

func askChoice(label string, choices []string, def string) (string, error) {
	return askUntil(ask, fmt.Sprintf("%s (%s)", label, strings.Join(choices, " | ")), def, func(v string) error {
		for _, c := range choices {
			if v == c {
				return nil
			}
		}
		return fmt.Errorf("please enter one of: %s", strings.Join(choices, ", "))
	})
}

// promptSecret reads a secret after label, echoing an asterisk for each
// character (so a paste is visibly received). Used for passphrases.
func promptSecret(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	if !isTerminal() {
		return readLine("")
	}
	v, err := withRaw(readMasked)
	return strings.TrimSpace(v), err
}

// promptSecretLong is promptSecret with the input on a fresh line.
func promptSecretLong(label string) (string, error) {
	fmt.Fprintln(os.Stderr, label+":")
	return promptSecret("> ")
}

// readMasked reads raw key presses until Enter, echoing '*' per character
// and handling backspace, Ctrl+U, Ctrl+C/Ctrl+D; escape sequences (arrow
// keys) are ignored.
func readMasked() (string, error) {
	var (
		secret  []rune
		buf     = make([]byte, 512)
		pending []byte
	)
	out := os.Stderr
	defer fmt.Fprint(out, "\r\n")
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return "", fmt.Errorf("reading input: %w", err)
		}
		data := append(pending, buf[:n]...)
		pending = nil
		for len(data) > 0 {
			switch b := data[0]; {
			case b == '\r' || b == '\n':
				return string(secret), nil
			case b == 3: // Ctrl+C
				return "", errCancelled
			case b == 4: // Ctrl+D
				if len(secret) == 0 {
					return "", errCancelled
				}
				data = data[1:]
			case b == 127 || b == 8: // backspace
				if len(secret) > 0 {
					secret = secret[:len(secret)-1]
					fmt.Fprint(out, "\b \b")
				}
				data = data[1:]
			case b == 21: // Ctrl+U: clear
				fmt.Fprint(out, strings.Repeat("\b \b", len(secret)))
				secret = secret[:0]
				data = data[1:]
			case b == 27: // escape sequence, e.g. an arrow key
				i := skipEscape(data)
				if i < 0 {
					pending = data
					data = nil
				} else {
					data = data[i:]
				}
			case b < 32:
				data = data[1:]
			default:
				if !utf8.FullRune(data) {
					pending = data
					data = nil
					continue
				}
				r, size := utf8.DecodeRune(data)
				secret = append(secret, r)
				fmt.Fprint(out, "*")
				data = data[size:]
			}
		}
	}
}

// skipEscape returns the length of the escape sequence at the start of
// data, or -1 if it is incomplete.
func skipEscape(data []byte) int {
	if len(data) < 2 {
		return -1
	}
	if data[1] != '[' && data[1] != 'O' {
		return 1 // lone ESC
	}
	for i := 2; i < len(data); i++ {
		if data[i] >= 0x40 && data[i] <= 0x7e {
			return i + 1
		}
	}
	return -1
}

// readPastedKey reads a pasted private key, line by line, up to its
// "-----END ... PRIVATE KEY-----" line. The key is echoed (multi-line
// input cannot be hidden).
func readPastedKey() (string, error) {
	if !isTerminal() {
		return readKeyLines(func() (string, error) {
			line, err := stdinLines.ReadString('\n')
			if err != nil && !(errors.Is(err, io.EOF) && line != "") {
				return "", fmt.Errorf("reading input: %w", err)
			}
			return line, nil
		})
	}
	return withRaw(func() (string, error) {
		t := term.NewTerminal(console{}, "") // one Terminal keeps pasted lines buffered
		return readKeyLines(func() (string, error) {
			line, err := t.ReadLine()
			if errors.Is(err, io.EOF) {
				return "", errCancelled
			}
			return line, err
		})
	})
}

func readKeyLines(next func() (string, error)) (string, error) {
	var b strings.Builder
	for {
		line, err := next()
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" && b.Len() == 0 {
			continue // blank lines before the key
		}
		if line == "" {
			return "", errors.New("the pasted key ended early (expected an \"-----END ... PRIVATE KEY-----\" line)")
		}
		b.WriteString(line + "\n")
		if strings.HasPrefix(line, "-----END ") && strings.HasSuffix(line, "PRIVATE KEY-----") {
			return b.String(), nil
		}
	}
}
