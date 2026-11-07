package repo

import (
	"context"
	"fmt"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
)

// CheckAccess confirms that url can be reached with auth by listing its
// branches, without downloading or writing anything.
func CheckAccess(ctx context.Context, url string, auth transport.AuthMethod) error {
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return fmt.Errorf("the repository is empty")
	}
	return nil
}
