package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TokenSource provides a GitHub API token. It exists so the data layer can
// inherit the operator's gh credentials in production while tests inject a
// static token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// ghTokenTimeout bounds the credential-bootstrap subprocess so a hung gh cannot
// strand a tool call.
const ghTokenTimeout = 10 * time.Second

// GHTokenSource sources the token from `gh auth token`, so overstory inherits
// the operator's existing gh authentication rather than managing its own. The
// token is fetched lazily on first use and cached for the process, guarded for
// concurrent tool calls. The token is a credential: it is never logged nor
// included in a returned error.
type GHTokenSource struct {
	mu     sync.Mutex
	cached string
}

// Token returns the gh token, fetching and caching it on first call.
func (s *GHTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" {
		return s.cached, nil
	}

	ctx, cancel := context.WithTimeout(ctx, ghTokenTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrGHNotFound
		}
		// gh exits non-zero when not logged in. Classify as an auth failure
		// without echoing stderr, which could carry sensitive detail.
		return "", fmt.Errorf("obtaining gh token: %w", ErrGHNotAuthed)
	}

	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", ErrGHNotAuthed
	}
	s.cached = token
	return token, nil
}
