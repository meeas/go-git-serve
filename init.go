package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/meeas/go-git-serve/internal/auth"
	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
)

var stdin = bufio.NewReader(os.Stdin)

func isTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// ask prompts for a line of input, returning def when the answer is empty.
func ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("reading input: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// askUntil repeats ask until check accepts the answer.
func askUntil(label, def string, check func(string) error) (string, error) {
	for {
		v, err := ask(label, def)
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
	return askUntil(fmt.Sprintf("%s (%s)", label, strings.Join(choices, " | ")), def, func(v string) error {
		for _, c := range choices {
			if v == c {
				return nil
			}
		}
		return fmt.Errorf("please enter one of: %s", strings.Join(choices, ", "))
	})
}

// promptSecret reads a secret from the terminal without echo.
func promptSecret(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func cmdInit(app string, args []string) error {
	f := newFlags(app)
	if err := f.parse(args); err != nil {
		return err
	}
	if !isTerminal() {
		return usageErr("init needs an interactive terminal; alternatively write the config file by hand (see README)")
	}
	cfg, paths, found, err := f.load(app)
	if err != nil {
		return err
	}
	if found {
		ok, err := ask(fmt.Sprintf("%s already exists. Overwrite? (y/N)", paths.ConfigFile), "N")
		if err != nil {
			return err
		}
		if !strings.EqualFold(ok, "y") && !strings.EqualFold(ok, "yes") {
			return nil
		}
	}
	_, err = wizard(app, paths, cfg)
	return err
}

// wizard asks for the main settings, starting from base, and writes the
// config file. Secrets go to the keychain or a separate file, never the config.
func wizard(app string, paths config.Paths, base config.Config) (config.Config, error) {
	c := base
	var err error

	if c.Repo.URL, err = askUntil("Repository URL (https://... or git@host:org/repo.git)", c.Repo.URL, func(v string) error {
		_, err := config.Endpoint(v)
		return err
	}); err != nil {
		return c, err
	}
	if c.Repo.Branch, err = ask("Branch (blank = repository default)", c.Repo.Branch); err != nil {
		return c, err
	}
	if c.Repo.Subdir, err = ask("Folder to serve (blank = repository root)", c.Repo.Subdir); err != nil {
		return c, err
	}

	def := c.Auth.Method
	if def == "" {
		def = config.InferAuthMethod(c)
	}
	if c.Auth.Method, err = askChoice("Authentication", config.AuthMethods, def); err != nil {
		return c, err
	}
	switch c.Auth.Method {
	case config.AuthSSHKey:
		if c.Auth.KeyFile, err = askUntil("SSH private key file", c.Auth.KeyFile, func(v string) error {
			if v == "" {
				return errors.New("a key file is required")
			}
			_, err := os.Stat(config.ExpandHome(v))
			return err
		}); err != nil {
			return c, err
		}
	case config.AuthToken:
		if err := tokenSetup(app, paths, &c); err != nil {
			return c, err
		}
	}

	if c.Server.Listen, err = ask("Listen address", c.Server.Listen); err != nil {
		return c, err
	}
	if c.Browser.Mode, err = askChoice("Open the site in", config.BrowserModes, c.Browser.Mode); err != nil {
		return c, err
	}

	check := c
	if err := config.Validate(&check); err != nil {
		return c, usageErr("%v", err)
	}
	if err := config.Write(paths.ConfigFile, c, true); err != nil {
		return c, fmt.Errorf("writing config: %w", err)
	}
	logx.Infof("Saved %s\n", paths.ConfigFile)
	return c, nil
}

// tokenSetup asks where the token should live and stores it there.
func tokenSetup(app string, paths config.Paths, c *config.Config) error {
	fmt.Fprintln(os.Stderr, "Use a fine-grained personal access token limited to this repository with read-only \"Contents\" permission.")
	where, err := askChoice("Store the token in", []string{"keychain", "file", "env"}, "keychain")
	if err != nil {
		return err
	}
	switch where {
	case "env":
		fmt.Fprintf(os.Stderr, "Set %s before running %s.\n", config.EnvToken, app)
		c.Auth.TokenFile = ""
		return nil
	case "keychain":
		tok, err := readToken()
		if err != nil {
			return err
		}
		if err := auth.SetToken(app, tok); err != nil {
			fmt.Fprintf(os.Stderr, "  could not use the OS keychain (%v); storing in a file instead\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "  token saved to the OS keychain")
			c.Auth.TokenFile = ""
			return nil
		}
		return writeTokenFile(paths, c, tok)
	default:
		tok, err := readToken()
		if err != nil {
			return err
		}
		return writeTokenFile(paths, c, tok)
	}
}

func readToken() (string, error) {
	tok, err := promptSecret("Token (input hidden): ")
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", usageErr("no token entered")
	}
	return tok, nil
}

func writeTokenFile(paths config.Paths, c *config.Config, tok string) error {
	def := c.Auth.TokenFile
	if def == "" {
		def = filepath.Join(filepath.Dir(paths.ConfigFile), paths.AppName+".token")
	}
	path, err := ask("Token file", def)
	if err != nil {
		return err
	}
	full := config.ExpandHome(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(full, []byte(tok+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}
	c.Auth.TokenFile = path
	fmt.Fprintf(os.Stderr, "  token saved to %s\n", full)
	return nil
}
