package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Regression pin for the review-round defect in JobRepoOriginPrefix: an
// origin originToPrefix could not reduce to `github.com/<owner>/<repo>` came
// back ok=true with a prefix no PR URL can equal, so /api/pr-attach refused
// every real PR of that checkout — fail CLOSED on the one machine-measured
// shape (an https origin carrying a userinfo, how a second GitHub account is
// cloned). Written by the lead after the review sighted the defect, so it is
// an implementer's pin, not a blind author's requirement test.
func originPinCheckout(t *testing.T, home, name, origin string) {
	t.Helper()
	dir := filepath.Join(home, "Developer", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if origin != "" {
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("git remote add: %v: %s", err, out)
		}
	}
}

func TestJobRepoOriginPrefixOnlyVouchesForACanonicalGitHubPrefix(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		wantPrefix string
		wantOK     bool
	}{
		{"https userinfo is stripped, still guarded", "https://someone@github.com/owner/repo.git", "github.com/owner/repo", true},
		{"ssh scheme is unknown", "ssh://git@github.com/owner/repo.git", "", false},
		{"ssh host alias is unknown", "git@gh-work:owner/repo.git", "", false},
		{"trailing slash is unknown", "https://github.com/owner/repo/", "", false},
		{"non-github host is unknown", "https://gitlab.com/owner/repo.git", "", false},
		{"plain https stays vouched", "https://github.com/owner/repo.git", "github.com/owner/repo", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			originPinCheckout(t, home, "pinrepo", c.origin)
			got, ok := JobRepoOriginPrefix(&Job{Slug: "pin", Repo: "pinrepo"})
			if got != c.wantPrefix || ok != c.wantOK {
				t.Fatalf("JobRepoOriginPrefix(origin %q) = (%q, %v), want (%q, %v)", c.origin, got, ok, c.wantPrefix, c.wantOK)
			}
		})
	}
}
