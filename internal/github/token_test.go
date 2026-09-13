package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGH stands in for the gh CLI. It models the part of gh's host resolution the
// token source must not depend on — an explicit --hostname wins, otherwise
// GH_HOST, otherwise github.com — prints a token only for a host named in
// FAKE_GH_LOGGED_IN, fails the way gh does for any other host, and appends each
// requested host to FAKE_GH_LOG. FAKE_GH_EMPTY makes it print a blank token,
// FAKE_GH_SLEEP makes it hang for that many seconds, and FAKE_GH_TOKEN_FILE makes a
// logged-in host's token whatever that file holds, so a test can change it between
// asks. Commands are builtins or absolute paths because tests put only the fake on
// PATH.
const fakeGH = `#!/bin/sh
host="${GH_HOST:-github.com}"
while [ $# -gt 0 ]; do
  case "$1" in
    --hostname) host="$2"; shift ;;
  esac
  shift
done
printf '%s\n' "$host" >> "$FAKE_GH_LOG"
if [ -n "$FAKE_GH_SLEEP" ]; then exec /bin/sleep "$FAKE_GH_SLEEP"; fi
if [ -n "$FAKE_GH_EMPTY" ]; then echo; exit 0; fi
for h in $FAKE_GH_LOGGED_IN; do
  if [ "$h" = "$host" ]; then
    if [ -n "$FAKE_GH_TOKEN_FILE" ]; then
      read -r token < "$FAKE_GH_TOKEN_FILE"
      echo "$token"
    else
      echo "token-for-$host"
    fi
    exit 0
  fi
done
echo "no oauth token found for $host" >&2
exit 1
`

// TestGHTokenSourceBindsTokenToAPIHost pins that the production fetcher asks gh
// for the token of the host its requests go to, whatever gh's default host is. A
// token issued by another host would be sent to a host that did not issue it.
func TestGHTokenSourceBindsTokenToAPIHost(t *testing.T) {
	prod := NewGraphQLFetcher()
	apiHost := endpointHost(t, prod.endpoint)
	if rest := endpointHost(t, prod.restEndpoint); rest != apiHost {
		t.Fatalf("GraphQL endpoint host %q and REST endpoint host %q differ", apiHost, rest)
	}
	issuer, ok := strings.CutPrefix(apiHost, "api.")
	if !ok {
		t.Fatalf("endpoint host %q is not an api. subdomain of the token's issuer", apiHost)
	}
	const otherHost = "ghe.example.com"

	for _, tc := range []struct {
		name      string
		ghHost    string
		loggedIn  string
		emptyOut  bool
		noGH      bool
		wantToken string
		wantErr   error
	}{
		{name: "gh default host is another host", ghHost: otherHost, loggedIn: issuer + " " + otherHost, wantToken: "token-for-" + issuer},
		{name: "only another host is logged in", ghHost: otherHost, loggedIn: otherHost, wantErr: ErrGHNotAuthed},
		{name: "gh prints an empty token", loggedIn: issuer, emptyOut: true, wantErr: ErrGHNotAuthed},
		{name: "gh not on PATH", noGH: true, wantErr: ErrGHNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := useFakeGH(t, tc.loggedIn)
			if tc.noGH {
				t.Setenv("PATH", t.TempDir())
			}
			t.Setenv("GH_HOST", tc.ghHost)
			if tc.emptyOut {
				t.Setenv("FAKE_GH_EMPTY", "1")
			}

			token, err := NewGraphQLFetcher().tokens.Token(t.Context())

			// Which host gh was asked for is checked on every path that reaches gh,
			// failures included, so a failing fetch cannot hide a wrong-host request.
			if !tc.noGH {
				logged, rerr := os.ReadFile(logPath)
				if rerr != nil {
					t.Fatalf("reading fake gh log: %v", rerr)
				}
				requested := strings.Fields(string(logged))
				if len(requested) == 0 {
					t.Error("gh was never asked for a token")
				}
				for _, host := range requested {
					if host != issuer {
						t.Errorf("gh was asked for host %q's token, want %q", host, issuer)
					}
				}
			}

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Token() error = %v, want %v", err, tc.wantErr)
				}
				if token != "" {
					t.Errorf("Token() returned a token alongside error %v", err)
				}
				if errors.Is(tc.wantErr, ErrGHNotAuthed) {
					// A literal, not githubHost: the operator-facing docs promise the
					// error names github.com, so a changed constant must fail here.
					if !strings.Contains(err.Error(), "github.com") {
						t.Errorf("error %q does not name github.com", err)
					}
					if strings.Contains(err.Error(), "no oauth token found") {
						t.Errorf("error %q echoes gh's stderr", err)
					}
				}
				if errors.Is(tc.wantErr, ErrGHNotFound) && !strings.Contains(err.Error(), "install gh") {
					t.Errorf("error %q does not name the fix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Token() error = %v, want nil", err)
			}
			if token != tc.wantToken {
				t.Errorf("Token() = %q, want %q", token, tc.wantToken)
			}
		})
	}
}

// TestGHTokenSourceInvalidate pins the compare-and-clear: invalidating a token the
// source no longer holds keeps its cache, so a caller that already fetched a newer
// token is not made to fetch again, while invalidating the held token sends the next
// ask back to gh.
func TestGHTokenSourceInvalidate(t *testing.T) {
	logPath := useFakeGH(t, "github.com")
	src := &GHTokenSource{}

	held, err := src.Token(t.Context())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	src.Invalidate("some-other-token")
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if n := ghInvocations(t, logPath); n != 1 {
		t.Errorf("gh ran %d times after invalidating a token the source does not hold, want 1", n)
	}

	src.Invalidate(held)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if n := ghInvocations(t, logPath); n != 2 {
		t.Errorf("gh ran %d times after invalidating the held token, want 2", n)
	}
}

// TestGHTokenSourceClassifiesFailures pins the causes that are not a missing login.
// A caller whose own context is done — a batch repository's deadline, say — gets that
// context's error, not a credential failure that would fail every repository with it.
// A gh that does not answer within the timeout gets its own error, since logging in
// again is not the fix for a hang.
func TestGHTokenSourceClassifiesFailures(t *testing.T) {
	t.Run("caller's context already done", func(t *testing.T) {
		useFakeGH(t, "github.com")
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()

		_, err := (&GHTokenSource{}).Token(ctx)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Token() error = %v, want context.DeadlineExceeded", err)
		}
		if errors.Is(err, ErrGHNotAuthed) || errors.Is(err, ErrGHTimedOut) {
			t.Errorf("Token() error = %v, reported as a credential failure", err)
		}
	})
	// A batch cancels its running fetches once one meets a failure every repository
	// shares. A token fetch cut short that way must not read as a credential failure,
	// which the batch could rank above the error that caused the cancellation.
	t.Run("caller's context cancelled", func(t *testing.T) {
		useFakeGH(t, "github.com")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := (&GHTokenSource{}).Token(ctx)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Token() error = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrGHNotAuthed) || errors.Is(err, ErrGHTimedOut) {
			t.Errorf("Token() error = %v, reported as a credential failure", err)
		}
	})
	t.Run("gh does not respond in time", func(t *testing.T) {
		useFakeGH(t, "github.com")
		t.Setenv("FAKE_GH_SLEEP", "5")

		_, err := (&GHTokenSource{timeout: 50 * time.Millisecond}).Token(t.Context())

		if !errors.Is(err, ErrGHTimedOut) {
			t.Fatalf("Token() error = %v, want ErrGHTimedOut", err)
		}
		if errors.Is(err, ErrGHNotAuthed) {
			t.Errorf("Token() error = %v, also reads as a missing login", err)
		}
		// The command a caller is pointed at must not print the token, as gh auth token
		// would; gh auth status reports on the stored credential without showing it.
		if !strings.Contains(err.Error(), "gh auth status --hostname github.com") {
			t.Errorf("error %q does not point at gh auth status for github.com", err)
		}
	})
}

// TestGHTokenSourceHealsRejectedTokenOnce pins the heal end to end through the real
// token source. Concurrent fetches that all start on a token GitHub rejects each
// succeed on the token gh hands out afterwards, and gh is asked for it once rather
// than once per rejected request.
func TestGHTokenSourceHealsRejectedTokenOnce(t *testing.T) {
	const fetches = 4
	logPath := useFakeGH(t, "github.com")
	tokenFile := filepath.Join(t.TempDir(), "token")
	writeToken := func(token string) {
		t.Helper()
		if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatalf("writing token file: %v", err)
		}
	}
	writeToken("stale-token")
	t.Setenv("FAKE_GH_TOKEN_FILE", tokenFile)

	arrived := make(chan struct{}, fetches)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var fresh atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "bearer stale-token":
			// Held until every fetch has arrived, so all of them start on the stale token.
			arrived <- struct{}{}
			<-release
			w.WriteHeader(http.StatusUnauthorized)
		case "bearer fresh-token":
			fresh.Add(1)
			if _, err := io.WriteString(w, "[]"); err != nil {
				t.Errorf("write response: %v", err)
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	// Registered after srv.Close so it runs first: a held handler would otherwise
	// block the server's shutdown when the test fails before releasing it.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	f := &GraphQLFetcher{restEndpoint: srv.URL, tokens: &GHTokenSource{}, client: &http.Client{}}
	errs := make(chan error, fetches)
	for range fetches {
		go func() {
			_, err := f.ListIssueEvents(context.Background(), "acme/widgets", time.Now().Add(-time.Hour), 100)
			errs <- err
		}()
	}
	for range fetches {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("not every fetch reached the server on the stale token")
		}
	}
	writeToken("fresh-token")
	releaseOnce.Do(func() { close(release) })

	for range fetches {
		if err := <-errs; err != nil {
			t.Errorf("fetch error = %v, want nil", err)
		}
	}
	if got := fresh.Load(); got != fetches {
		t.Errorf("requests on the replacement token = %d, want %d", got, fetches)
	}
	if n := ghInvocations(t, logPath); n != 2 {
		t.Errorf("gh ran %d times, want 2: the first ask and one replacement shared by every rejected request", n)
	}
}

// useFakeGH puts the fake gh alone on PATH with the given hosts logged in and every
// other fake switch off, and returns the file it logs each invocation to.
func useFakeGH(t *testing.T, loggedIn string) string {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(fakeGH), 0o700); err != nil {
		t.Fatalf("writing fake gh: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "hosts.log")
	t.Setenv("PATH", binDir)
	t.Setenv("FAKE_GH_LOG", logPath)
	t.Setenv("FAKE_GH_LOGGED_IN", loggedIn)
	for _, name := range []string{"GH_HOST", "FAKE_GH_EMPTY", "FAKE_GH_SLEEP", "FAKE_GH_TOKEN_FILE"} {
		t.Setenv(name, "")
	}
	return logPath
}

// ghInvocations counts the fake gh's runs from its log; a log never written is none.
func ghInvocations(t *testing.T, logPath string) int {
	t.Helper()
	logged, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("reading fake gh log: %v", err)
	}
	return len(strings.Fields(string(logged)))
}

func endpointHost(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parsing endpoint %q: %v", endpoint, err)
	}
	return u.Hostname()
}
