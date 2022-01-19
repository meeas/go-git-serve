package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	. "github.com/go-git/go-git/v5/_examples"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/ilyakaznacheev/cleanenv"
)

type ConfigDatabase struct {
	gitUrl      string `yaml:"gitUrl" env:"GGS-GITURL"`
	sshPrivKey  []byte `yaml:"sshPrivateKey" env:"GGS-SSHPRIVATEKEY"`
	webAddrPort string `yaml:"webAddrPort" env:"GGS-WEBSERVERPORT" env-default:"localhost:8888"`
}

func getConfigFile(exeFilename string) string {
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	return userConfigDir + "/" + exeFilename + ".yaml"
}

func getGitWebRoot(exeFilename string) string {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	return userCacheDir + "/" + exeFilename
}

func createConfigFile(userConfigFile string, cfg ConfigDatabase) {
	fmt.Println("Configuration file not found.")
	fmt.Printf("Git repository URL: ")
	fmt.Scanln(&cfg.gitUrl)
	fmt.Printf("SSH private key file: ")
	fmt.Scanln(&cfg.sshPrivKey)
}

func gitSshClone(url, directory string, sshPrivKey []byte) {
	var password string

	// Clone the given repository to the given directory
	Info("git clone %s ", url)
	publicKeys, err := ssh.NewPublicKeys("git", sshPrivKey, password)
	if err != nil {
		Warning("generate publickeys failed: %s\n", err.Error())
		return
	}

	_, err = git.PlainClone(directory, false, &git.CloneOptions{
		// The intended use of a GitHub personal access token is in replace of your password
		// because access tokens can easily be revoked.
		// https://help.github.com/articles/creating-a-personal-access-token-for-the-command-line/
		Auth:     publicKeys,
		URL:      url,
		Progress: os.Stdout,
	})
	if err != nil {
		fmt.Println("Content successfully cloned.")
	}
}

func gitSshPull(webRoot string, sshPrivKey []byte) {
	var password string

	// We instantiate a new repository targeting the given path (the .git folder)
	r, err := git.PlainOpen(webRoot)
	CheckIfError(err)

	// Get the working directory for the repository
	w, err := r.Worktree()
	CheckIfError(err)

	publicKeys, err := ssh.NewPublicKeys("git", sshPrivKey, password)
	if err != nil {
		Warning("generate publickeys failed: %s\n", err.Error())
		return
	}

	// Pull the latest changes from the origin remote and merge into the current branch
	err = w.Pull(&git.PullOptions{
		RemoteName: "origin",
		Auth:       publicKeys,
	})
	if err != nil {
		fmt.Println("Content updated.")
	}

}

func main() {

	var cfg ConfigDatabase

	exeFilename := filepath.Base(os.Args[0])
	userConfigFile := getConfigFile(exeFilename)
	userGitWebRoot := getGitWebRoot(exeFilename)

	if _, err := os.Stat(userConfigFile); os.IsNotExist(err) {
		fmt.Println(err)
		os.Exit(2)
	}

	err := cleanenv.ReadConfig(userConfigFile, &cfg)
	if err != nil {
		fmt.Println(err)
	}

	fmt.Println(cfg.gitUrl)
	fmt.Println(cfg.sshPrivKey)
	fmt.Println(cfg.webAddrPort)

	if _, err := os.Stat(userGitWebRoot); os.IsNotExist(err) {
		gitSshClone(cfg.gitUrl, userGitWebRoot, cfg.sshPrivKey)
	} else {
		gitSshPull(userGitWebRoot, cfg.sshPrivKey)
	}

	fmt.Println("Starting webserver on http://" + cfg.webAddrPort)
	http.Handle("/", http.FileServer(http.Dir(userGitWebRoot)))
	log.Fatal(http.ListenAndServe(cfg.webAddrPort, nil))
}
