package github

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH stands in for the gh CLI. It models the part of gh's host resolution the
// token source must not depend on — an explicit --hostname wins, otherwise
// GH_HOST, otherwise github.com — prints a token only for a host named in
// FAKE_GH_LOGGED_IN, fails the way gh does for any other host, and appends each
// requested host to FAKE_GH_LOG. FAKE_GH_EMPTY makes it print a blank token.
const fakeGH = `#!/bin/sh
host="${GH_HOST:-github.com}"
while [ $# -gt 0 ]; do
  case "$1" in
    --hostname) host="$2"; shift ;;
  esac
  shift
done
printf '%s\n' "$host" >> "$FAKE_GH_LOG"
if [ -n "$FAKE_GH_EMPTY" ]; then echo; exit 0; fi
for h in $FAKE_GH_LOGGED_IN; do
  if [ "$h" = "$host" ]; then echo "token-for-$host"; exit 0; fi
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
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "hosts.log")
			if !tc.noGH {
				if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(fakeGH), 0o700); err != nil {
					t.Fatalf("writing fake gh: %v", err)
				}
			}
			t.Setenv("PATH", binDir)
			t.Setenv("FAKE_GH_LOG", logPath)
			t.Setenv("FAKE_GH_LOGGED_IN", tc.loggedIn)
			t.Setenv("GH_HOST", tc.ghHost)
			if tc.emptyOut {
				t.Setenv("FAKE_GH_EMPTY", "1")
			} else {
				t.Setenv("FAKE_GH_EMPTY", "")
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

func endpointHost(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parsing endpoint %q: %v", endpoint, err)
	}
	return u.Hostname()
}
