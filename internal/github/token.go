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
// static token. Invalidate reports that the API rejected token, so a source that
// caches drops it and its next Token asks afresh.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	Invalidate(token string)
}

// ghTokenTimeout bounds each gh run fetching the credential, so a hung gh cannot
// strand a tool call.
const ghTokenTimeout = 10 * time.Second

// GHTokenSource sources the token from `gh auth token`, so overstory inherits
// the operator's existing gh authentication rather than managing its own. It passes
// --hostname githubHost because, without it, `gh auth token` resolves go-gh's
// auth.DefaultHost — GH_HOST, else the one configured host — so a GH_HOST setting or
// a lone Enterprise Server login would hand back that host's token, to be sent to a
// host that did not issue it. The token is fetched lazily on first use and cached
// until the API answers 401 for it (see Invalidate), guarded for concurrent tool
// calls. The token is a credential: it is never logged nor included in a returned
// error.
type GHTokenSource struct {
	mu     sync.Mutex
	cached string
	// timeout bounds each gh run; zero means ghTokenTimeout.
	timeout time.Duration
}

// Token returns the cached token, or fetches one from gh and caches it.
func (s *GHTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" {
		return s.cached, nil
	}

	timeout := s.timeout
	if timeout <= 0 {
		timeout = ghTokenTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "gh", "auth", "token", "--hostname", githubHost)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return "", ErrGHNotFound
		case ctx.Err() != nil:
			// The caller's own deadline or cancellation cut the run short, which says
			// nothing about the credential: a batch repository's deadline must fail that
			// repository alone. Checked before runCtx, which inherits it.
			return "", fmt.Errorf("obtaining gh token: %w", ctx.Err())
		case runCtx.Err() != nil:
			return "", fmt.Errorf("obtaining gh token after waiting %s: %w", timeout, ErrGHTimedOut)
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

// Invalidate drops token from the cache if the cache still holds it. Comparing
// first matters when concurrent requests are rejected together: the first to report
// clears the cache and the next Token fetches a replacement, which a later report of
// the same rejected token must not throw away.
func (s *GHTokenSource) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == token {
		s.cached = ""
	}
}
