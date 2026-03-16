package collectors

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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

func (r *goGitRepo) BranchCommit(branch string) (*CommitInfo, string, error) {
	// Try remote ref first, then local
	ref, err := r.repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		ref, err = r.repo.Reference(plumbing.NewBranchReferenceName(branch), true)
		if err != nil {
			return nil, "", fmt.Errorf("branch %s not found: %w", branch, err)
		}
	}
	hash := ref.Hash().String()
	commit, err := r.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, "", err
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
	}, hash, nil
}

func (r *goGitRepo) IsAncestor(commitHash, branchRef string) (bool, error) {
	commitObj, err := r.repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return false, err
	}
	ref, err := r.repo.Reference(plumbing.NewRemoteReferenceName("origin", branchRef), true)
	if err != nil {
		// Try local branch
		ref, err = r.repo.Reference(plumbing.NewBranchReferenceName(branchRef), true)
		if err != nil {
			return false, err
		}
	}
	branchCommit, err := r.repo.CommitObject(ref.Hash())
	if err != nil {
		return false, err
	}
	return commitObj.IsAncestor(branchCommit)
}

func (r *goGitRepo) CommitCounts(headHash, trackedHash string) (ahead int, behind int, err error) {
	headCommit, err := r.repo.CommitObject(plumbing.NewHash(headHash))
	if err != nil {
		return 0, 0, err
	}
	trackedCommit, err := r.repo.CommitObject(plumbing.NewHash(trackedHash))
	if err != nil {
		return 0, 0, err
	}

	// Find merge base by collecting all ancestors
	headAncestors := make(map[plumbing.Hash]bool)
	trackedAncestors := make(map[plumbing.Hash]bool)

	// Walk HEAD's history
	iter, err := r.repo.Log(&git.LogOptions{From: headCommit.Hash})
	if err != nil {
		return 0, 0, err
	}
	_ = iter.ForEach(func(c *object.Commit) error {
		headAncestors[c.Hash] = true
		return nil
	})

	// Walk tracked branch's history
	iter, err = r.repo.Log(&git.LogOptions{From: trackedCommit.Hash})
	if err != nil {
		return 0, 0, err
	}
	_ = iter.ForEach(func(c *object.Commit) error {
		trackedAncestors[c.Hash] = true
		return nil
	})

	// Ahead = commits in HEAD not in tracked
	for h := range headAncestors {
		if !trackedAncestors[h] {
			ahead++
		}
	}
	// Behind = commits in tracked not in HEAD
	for h := range trackedAncestors {
		if !headAncestors[h] {
			behind++
		}
	}

	return ahead, behind, nil
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

	if remoteURL, err := repo.RemoteURL("origin"); err == nil {
		gc.RemoteURL = sanitizeRemoteURL(remoteURL)
	}

	gc.TrackedRepo = c.TrackedRepo

	// Validate repo: skip compliance check if running from wrong repository
	if c.TrackedRepo != "" && gc.RemoteURL != "" && !repoMatches(gc.RemoteURL, c.TrackedRepo) {
		gc.RepoMismatch = true
		return gc
	}

	// Tracked branch compliance check
	if c.TrackedBranch != "" {
		if info, hash, err := repo.BranchCommit(c.TrackedBranch); err == nil {
			gc.TrackedBranch = c.TrackedBranch
			gc.TrackedCommit = hash
			if len(hash) >= 7 {
				gc.TrackedCommitShort = hash[:7]
			}
			gc.TrackedCommitAuthor = info.Author
			gc.TrackedCommitEmail = info.Email
			gc.TrackedCommitMsg = info.Message
			gc.TrackedCommitTime = info.Timestamp
			gc.IsCurrentBranch = gc.Branch == c.TrackedBranch

			if gc.Commit != "" {
				if merged, err := repo.IsAncestor(gc.Commit, c.TrackedBranch); err == nil {
					gc.IsMerged = merged
				}
				if ahead, behind, err := repo.CommitCounts(gc.Commit, hash); err == nil {
					gc.CommitsAhead = ahead
					gc.CommitsBehind = behind
				}
			}
		}
	}

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

	branch := run("rev-parse", "--abbrev-ref", "HEAD")
	if branch == "HEAD" {
		gc.Branch = c.resolveBranchFromCI()
	} else {
		gc.Branch = branch
	}

	gc.Dirty = run("status", "--porcelain") != ""

	if remoteURL := run("remote", "get-url", "origin"); remoteURL != "" {
		gc.RemoteURL = sanitizeRemoteURL(remoteURL)
	}

	gc.TrackedRepo = c.TrackedRepo

	// Validate repo: skip compliance check if running from wrong repository
	if c.TrackedRepo != "" && gc.RemoteURL != "" && !repoMatches(gc.RemoteURL, c.TrackedRepo) {
		gc.RepoMismatch = true
		return gc
	}

	// Tracked branch compliance check
	if c.TrackedBranch != "" {
		trackedRef := "refs/remotes/origin/" + c.TrackedBranch
		hash := run("rev-parse", trackedRef)
		if hash == "" {
			trackedRef = "refs/heads/" + c.TrackedBranch
			hash = run("rev-parse", trackedRef)
		}
		if hash != "" {
			gc.TrackedBranch = c.TrackedBranch
			gc.TrackedCommit = hash
			if len(hash) >= 7 {
				gc.TrackedCommitShort = hash[:7]
			}
			gc.IsCurrentBranch = gc.Branch == c.TrackedBranch

			gc.TrackedCommitAuthor = run("log", "-1", "--format=%an", hash)
			gc.TrackedCommitEmail = run("log", "-1", "--format=%ae", hash)
			msg := run("log", "-1", "--format=%B", hash)
			if len(msg) > 200 {
				msg = msg[:200]
			}
			gc.TrackedCommitMsg = msg
			if ts := run("log", "-1", "--format=%aI", hash); ts != "" {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					gc.TrackedCommitTime = t
				}
			}

			// Ancestry check: is HEAD already in tracked branch?
			_, mergeErr := c.Cmd.Run(ctx, dir, "git", "merge-base", "--is-ancestor", "HEAD", trackedRef)
			gc.IsMerged = mergeErr == nil

			// Divergence: ahead/behind counts
			if counts := run("rev-list", "--left-right", "--count", "HEAD..."+trackedRef); counts != "" {
				parts := strings.Fields(counts)
				if len(parts) == 2 {
					gc.CommitsAhead, _ = strconv.Atoi(parts[0])
					gc.CommitsBehind, _ = strconv.Atoi(parts[1])
				}
			}
		}
	}

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

// normalizeRepoURL normalizes a Git remote URL for comparison.
// Handles HTTPS, SSH, and .git suffix variations so that:
//   - https://github.com/org/repo.git
//   - https://github.com/org/repo
//   - git@github.com:org/repo.git
//
// all normalize to "github.com/org/repo".
func normalizeRepoURL(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	s = strings.TrimSuffix(s, ".git")

	// SSH: git@github.com:org/repo → github.com/org/repo
	if strings.Contains(s, "@") && !strings.Contains(s, "://") {
		if idx := strings.Index(s, "@"); idx >= 0 {
			s = s[idx+1:]
		}
		s = strings.Replace(s, ":", "/", 1)
		return strings.ToLower(s)
	}

	// HTTPS: https://github.com/org/repo → github.com/org/repo
	parsed, err := url.Parse(s)
	if err != nil {
		return strings.ToLower(s)
	}
	return strings.ToLower(strings.TrimPrefix(parsed.Host+parsed.Path, "/"))
}

// repoMatches checks if the detected git remote URL matches the user-configured tracked_repo.
func repoMatches(detectedRemote, trackedRepo string) bool {
	if detectedRemote == "" || trackedRepo == "" {
		return false
	}
	return normalizeRepoURL(detectedRemote) == normalizeRepoURL(trackedRepo)
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
