package ghauthcmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/onelastcommit/noctra/internal/ghauth"
)

const credentialTimeout = 30 * time.Second

func GitCredential(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	dir, scopeName, action := "", string(ghauth.ScopeGit), ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case "--scope":
			if i+1 < len(args) {
				scopeName = args[i+1]
				i++
			}
		default:
			action = args[i]
		}
	}
	if action != "get" {
		return nil
	}
	if dir == "" {
		return fmt.Errorf("git-credential: --dir is required")
	}
	scope, err := ghauth.ParseScope(scopeName)
	if err != nil {
		return err
	}
	attrs, err := ghauth.ParseCredentialInput(stdin)
	if err != nil {
		return err
	}
	repo, err := ghauth.RepoFromCredential(attrs)
	if err != nil {
		fmt.Fprintf(stderr, "noctra git-credential: %v\n", err)
		return nil
	}
	sess, err := ghauth.OpenSession(dir, "")
	if err != nil {
		fmt.Fprintf(stderr, "noctra git-credential: %v\n", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), credentialTimeout)
	defer cancel()
	tok, err := sess.FreshToken(ctx, repo, scope)
	if err != nil {
		fmt.Fprintf(stderr, "noctra git-credential: no %s token for %s: %v\n", scope, repo, err)
		return nil
	}
	_, err = io.WriteString(stdout, ghauth.FormatCredential(tok.Token, tok.ExpiresAt))
	return err
}
