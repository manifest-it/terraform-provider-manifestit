package collectors

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// --- go-git backed GitOpener / GitRepo ---

type goGitOpener struct{}

func (goGitOpener) Open(dir string) (GitRepo, error) {
	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return nil, err
	}
	return &goGitRepo{repo: repo}, nil
}

type goGitRepo struct {
	repo *git.Repository
}

func (r *goGitRepo) HeadHash() (string, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

func (r *goGitRepo) HeadBranch() (string, bool, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return "", false, err
	}
	if ref.Name().IsBranch() {
		return ref.Name().Short(), false, nil
	}
	return "HEAD", true, nil
}

func (r *goGitRepo) IsDirty() (bool, error) {
	w, err := r.repo.Worktree()
	if err != nil {
		return false, err
	}
	status, err := w.Status()
	if err != nil {
		return false, err
	}
	return !status.IsClean(), nil
}

func (r *goGitRepo) HeadCommit() (*CommitInfo, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return nil, err
	}
	commit, err := r.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, err
	}
	msg := commit.Message
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &CommitInfo{
		Author:    commit.Author.Name,
		Email:     commit.Author.Email,
		Message:   msg,
		Timestamp: commit.Author.When,
	}, nil
}

func (r *goGitRepo) RemoteURL(name string) (string, error) {
	remote, err := r.repo.Remote(name)
	if err != nil {
		return "", err
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return "", fmt.Errorf("no URLs for remote %s", name)
	}
	return urls[0], nil
}

func (r *goGitRepo) TagsAtHead() ([]string, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return nil, err
	}
	headHash := ref.Hash()

	tagRefs, err := r.repo.Tags()
	if err != nil {
		return nil, err
	}

	var result []string
	_ = tagRefs.ForEach(func(ref *plumbing.Reference) error {
		// Lightweight tags point directly at the commit
		if ref.Hash() == headHash {
			result = append(result, ref.Name().Short())
			return nil
		}
		// Annotated tags: resolve the tag object to find the target commit
		tagObj, err := r.repo.TagObject(ref.Hash())
		if err == nil && tagObj.Target == headHash {
			result = append(result, ref.Name().Short())
		}
		return nil
	})

	return result, nil
}

func (r *goGitRepo) ConfigValue(section, subsection, key string) (string, error) {
	cfg, err := r.repo.Config()
	if err != nil {
		return "", err
	}
	raw := cfg.Raw
	if raw == nil {
		return "", fmt.Errorf("no raw config")
	}
	sec := raw.Section(section)
	if subsection != "" {
		return sec.Subsection(subsection).Option(key), nil
	}
	return sec.Option(key), nil
}

func (r *goGitRepo) Close() {}

// --- Git context collection ---

// CollectGitContext collects Git provenance using go-git (primary) with CLI fallback.
func (c *Collector) CollectGitContext(ctx context.Context) GitContext {
	dir, err := findGitDir()
	if err != nil {
		tflog.Debug(ctx, "no git repository found", map[string]interface{}{"error": err.Error()})
		return GitContext{Available: false}
	}

	// Try go-git first
	opener := c.GitOp
	if opener == nil {
		opener = goGitOpener{}
	}

	repo, err := opener.Open(dir)
	if err == nil {
		defer repo.Close()
		return c.collectGitFromRepo(ctx, repo)
	}
	tflog.Debug(ctx, "go-git failed, falling back to CLI", map[string]interface{}{"error": err.Error()})

	// Fallback to CLI
	return c.collectGitFromCLI(ctx, dir)
}

func (c *Collector) collectGitFromRepo(ctx context.Context, repo GitRepo) GitContext {
	gc := GitContext{Available: true}

	if hash, err := repo.HeadHash(); err == nil {
		gc.Commit = hash
		if len(hash) >= 7 {
			gc.CommitShort = hash[:7]
		}
	}

	if branch, detached, err := repo.HeadBranch(); err == nil {
		if detached {
			gc.Branch = c.resolveBranchFromCI()
		} else {
			gc.Branch = branch
		}
	}

	if dirty, err := repo.IsDirty(); err == nil {
		gc.Dirty = dirty
	}

	if info, err := repo.HeadCommit(); err == nil {
		gc.CommitAuthor = info.Author
		gc.CommitEmail = info.Email
		gc.CommitMessage = info.Message
		gc.CommitTimestamp = info.Timestamp
	}

	if c.Config.GitIdentity {
		gc.LocalGitName, _ = repo.ConfigValue("user", "", "name")
		gc.LocalGitEmail, _ = repo.ConfigValue("user", "", "email")
	}

	if remoteURL, err := repo.RemoteURL("origin"); err == nil {
		gc.RemoteURL = sanitizeRemoteURL(remoteURL)
	}

	if tags, err := repo.TagsAtHead(); err == nil {
		gc.Tags = tags
	}

	gc.PRNumber = c.detectPRNumber()

	return gc
}

func (c *Collector) collectGitFromCLI(ctx context.Context, dir string) GitContext {
	gc := GitContext{Available: true}

	run := func(args ...string) string {
		out, err := c.Cmd.Run(ctx, dir, "git", args...)
		if err != nil {
			return ""
		}
		return out
	}

	gc.Commit = run("rev-parse", "HEAD")
	if len(gc.Commit) >= 7 {
		gc.CommitShort = gc.Commit[:7]
	}

	branch := run("rev-parse", "--abbrev-ref", "HEAD")
	if branch == "HEAD" {
		gc.Branch = c.resolveBranchFromCI()
	} else {
		gc.Branch = branch
	}

	gc.Dirty = run("status", "--porcelain") != ""
	gc.CommitAuthor = run("log", "-1", "--format=%an")
	gc.CommitEmail = run("log", "-1", "--format=%ae")

	msg := run("log", "-1", "--format=%B")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	gc.CommitMessage = msg

	if ts := run("log", "-1", "--format=%aI"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			gc.CommitTimestamp = t
		}
	}

	if c.Config.GitIdentity {
		gc.LocalGitName = run("config", "user.name")
		gc.LocalGitEmail = run("config", "user.email")
	}

	if remoteURL := run("remote", "get-url", "origin"); remoteURL != "" {
		gc.RemoteURL = sanitizeRemoteURL(remoteURL)
	}

	if tagOutput := run("tag", "--points-at", "HEAD"); tagOutput != "" {
		gc.Tags = strings.Split(tagOutput, "\n")
	}

	gc.PRNumber = c.detectPRNumber()

	return gc
}

// resolveBranchFromCI attempts to determine the branch from CI env vars when HEAD is detached.
func (c *Collector) resolveBranchFromCI() string {
	candidates := []struct {
		env   string
		strip string // prefix to strip
	}{
		{"GITHUB_HEAD_REF", ""},               // GitHub Actions (PRs)
		{"GITHUB_REF_NAME", ""},               // GitHub Actions (pushes)
		{"CI_COMMIT_REF_NAME", ""},            // GitLab CI
		{"GIT_BRANCH", "origin/"},             // Jenkins
		{"BUILD_SOURCEBRANCH", "refs/heads/"}, // Azure DevOps
		{"CIRCLE_BRANCH", ""},                 // CircleCI
		{"BITBUCKET_BRANCH", ""},              // Bitbucket Pipelines
	}

	for _, cand := range candidates {
		val := c.Env.Getenv(cand.env)
		if val == "" {
			continue
		}
		if cand.strip != "" {
			val = strings.TrimPrefix(val, cand.strip)
		}
		return val
	}
	return ""
}

// detectPRNumber extracts PR/MR number from CI environment variables.
func (c *Collector) detectPRNumber() string {
	prEnvVars := []string{
		"CI_MERGE_REQUEST_IID",             // GitLab
		"SYSTEM_PULLREQUEST_PULLREQUESTID", // Azure DevOps
		"BITBUCKET_PR_ID",                  // Bitbucket
		"ghprbPullId",                      // Jenkins GitHub PR Builder
	}

	for _, env := range prEnvVars {
		if val := c.Env.Getenv(env); val != "" {
			return val
		}
	}

	// GitHub Actions: parse PR number from GITHUB_REF (refs/pull/123/merge)
	if c.Env.Getenv("GITHUB_ACTIONS") == "true" {
		ref := c.Env.Getenv("GITHUB_REF")
		if strings.HasPrefix(ref, "refs/pull/") {
			parts := strings.Split(ref, "/")
			if len(parts) >= 3 {
				return parts[2]
			}
		}
	}

	return ""
}

// findGitDir walks up from the working directory looking for .git.
func findGitDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() || info.Mode().IsRegular() {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git found")
		}
		dir = parent
	}
}

// sanitizeRemoteURL strips credentials from a Git remote URL.
// e.g. https://token@github.com/org/repo → https://github.com/org/repo
func sanitizeRemoteURL(rawURL string) string {
	// SSH URLs (git@github.com:org/repo.git) — no embedded credentials to strip
	if strings.Contains(rawURL, "@") && !strings.Contains(rawURL, "://") {
		return rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.User = nil
	return parsed.String()
}
