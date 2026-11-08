package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/meeas/go-git-serve/internal/auth"
	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
)

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
// config file. A token or pasted key can be kept in the config itself
// (inline), or in the keychain or a separate file.
func wizard(app string, paths config.Paths, base config.Config) (config.Config, error) {
	c := base
	var err error

	if c.Repo.URL, err = askUntil(askLong, "Repository URL (https://... or git@host:org/repo.git)", c.Repo.URL, func(v string) error {
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
		c.Auth.TokenFile, c.Auth.Token = "", ""
		if err := keySetup(app, paths, &c); err != nil {
			return c, err
		}
	case config.AuthToken:
		c.Auth.KeyFile, c.Auth.Key = "", ""
		if err := tokenSetup(app, paths, &c); err != nil {
			return c, err
		}
	default:
		c.Auth.KeyFile, c.Auth.Key, c.Auth.TokenFile, c.Auth.Token = "", "", "", ""
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

// keySetup asks for an SSH key: a path to a key file, or a pasted key kept
// inline in the config or saved as a file next to it.
func keySetup(app string, paths config.Paths, c *config.Config) error {
	how, err := askChoice("Provide the key as", []string{"file", "paste"}, "file")
	if err != nil {
		return err
	}
	if how == "file" {
		c.Auth.Key = ""
		c.Auth.KeyFile, err = askUntil(askLong, "SSH private key file", c.Auth.KeyFile, func(v string) error {
			if v == "" {
				return errors.New("a key file is required")
			}
			_, err := os.Stat(config.ExpandHome(v))
			return err
		})
		return err
	}

	var key string
	for {
		fmt.Fprintln(os.Stderr, "Paste the private key below; it is shown on screen while pasting.")
		fmt.Fprintln(os.Stderr, `Input ends at the "-----END ... PRIVATE KEY-----" line.`)
		if key, err = readPastedKey(); err != nil {
			if errors.Is(err, errCancelled) {
				return err
			}
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			continue
		}
		_, perr := ssh.ParsePrivateKey([]byte(key))
		var missing *ssh.PassphraseMissingError
		if errors.As(perr, &missing) {
			fmt.Fprintf(os.Stderr, "  This key has a passphrase; you'll be asked for it at each start, or set %s.\n", config.EnvPassphrase)
			fmt.Fprintln(os.Stderr, "  Services need a key without one.")
			break
		}
		if perr != nil {
			fmt.Fprintf(os.Stderr, "  that is not a valid private key (%v); try again\n", perr)
			continue
		}
		break
	}

	where, err := askChoice("Store the key in", []string{"config", "file"}, "config")
	if err != nil {
		return err
	}
	if where == "config" {
		c.Auth.Key, c.Auth.KeyFile = key, ""
		fmt.Fprintln(os.Stderr, "  the key will be saved inside the config file")
		return nil
	}
	path, err := askLong("Save the key as", filepath.Join(filepath.Dir(paths.ConfigFile), paths.AppName+".key"))
	if err != nil {
		return err
	}
	full := config.ExpandHome(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(full, []byte(key), 0o600); err != nil {
		return fmt.Errorf("writing key file: %w", err)
	}
	c.Auth.KeyFile, c.Auth.Key = path, ""
	fmt.Fprintf(os.Stderr, "  key saved to %s\n", full)
	return nil
}

// tokenSetup asks where the token should live and stores it there.
func tokenSetup(app string, paths config.Paths, c *config.Config) error {
	fmt.Fprintln(os.Stderr, "Use a fine-grained personal access token limited to this repository with read-only \"Contents\" permission.")
	where, err := askChoice("Store the token in", []string{"config", "keychain", "file", "env"}, "keychain")
	if err != nil {
		return err
	}
	c.Auth.Token, c.Auth.TokenFile = "", ""
	switch where {
	case "env":
		fmt.Fprintf(os.Stderr, "Set %s before running %s.\n", config.EnvToken, app)
		return nil
	case "config":
		tok, err := readToken()
		if err != nil {
			return err
		}
		c.Auth.Token = tok
		fmt.Fprintln(os.Stderr, "  the token will be saved inside the config file")
		return nil
	case "keychain":
		tok, err := readToken()
		if err != nil {
			return err
		}
		if err := auth.SetToken(app, tok); err != nil {
			fmt.Fprintf(os.Stderr, "  could not use the OS keychain (%v); storing in a file instead\n", err)
			return writeTokenFile(paths, c, tok)
		}
		fmt.Fprintln(os.Stderr, "  token saved to the OS keychain")
		return nil
	default:
		tok, err := readToken()
		if err != nil {
			return err
		}
		return writeTokenFile(paths, c, tok)
	}
}

func readToken() (string, error) {
	tok, err := promptSecretLong("Token (shown as *)")
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", usageErr("no token entered")
	}
	return tok, nil
}

func writeTokenFile(paths config.Paths, c *config.Config, tok string) error {
	def := filepath.Join(filepath.Dir(paths.ConfigFile), paths.AppName+".token")
	path, err := askLong("Token file", def)
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
