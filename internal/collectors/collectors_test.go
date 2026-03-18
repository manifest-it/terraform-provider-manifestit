package collectors

import (
	"context"
	"fmt"
	"os/user"
	"testing"
	"time"
)

// --- Mock dependencies ---

type mockEnvReader struct {
	vars map[string]string
}

func (m *mockEnvReader) Getenv(key string) string { return m.vars[key] }

type mockUserLookup struct {
	user *user.User
	err  error
}

func (m *mockUserLookup) Current() (*user.User, error) { return m.user, m.err }

type mockHostnameResolver struct {
	hostname string
	err      error
}

func (m *mockHostnameResolver) Hostname() (string, error) { return m.hostname, m.err }

type mockCommandRunner struct {
	outputs map[string]string // key is "arg0 arg1 ..." → output
}

func (m *mockCommandRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	if out, ok := m.outputs[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("command not mocked: %s", key)
}

type mockGitRepo struct {
	hash          string
	branch        string
	detached      bool
	dirty         bool
	remote        string
	branchCommits map[string]struct { // keyed by branch name
		info *CommitInfo
		hash string
	}
	isAncestorResult bool
	commitsAhead     int
	commitsBehind    int
}

func (m *mockGitRepo) HeadHash() (string, error) { return m.hash, nil }
func (m *mockGitRepo) HeadBranch() (string, bool, error) {
	if m.detached {
		return "HEAD", true, nil
	}
	return m.branch, false, nil
}
func (m *mockGitRepo) IsDirty() (bool, error)            { return m.dirty, nil }
func (m *mockGitRepo) RemoteURL(_ string) (string, error) { return m.remote, nil }
func (m *mockGitRepo) BranchCommit(branch string) (*CommitInfo, string, error) {
	if bc, ok := m.branchCommits[branch]; ok {
		return bc.info, bc.hash, nil
	}
	return nil, "", fmt.Errorf("branch %s not found", branch)
}
func (m *mockGitRepo) IsAncestor(_, _ string) (bool, error) {
	return m.isAncestorResult, nil
}
func (m *mockGitRepo) CommitCounts(_, _ string) (int, int, error) {
	return m.commitsAhead, m.commitsBehind, nil
}
func (m *mockGitRepo) Close() {}

type mockGitOpener struct {
	repo GitRepo
	err  error
}

func (m *mockGitOpener) Open(_ string) (GitRepo, error) { return m.repo, m.err }

// --- Tests ---

func TestCollectLocalIdentity_Local(t *testing.T) {
	c := &Collector{
		Config: CollectConfig{OSUser: true, Hostname: true},
		Env:    &mockEnvReader{vars: map[string]string{}},
		User: &mockUserLookup{user: &user.User{
			Username: "testuser",
			Uid:      "1001",
			HomeDir:  "/home/testuser",
		}},
		Host: &mockHostnameResolver{hostname: "dev-laptop.local"},
	}

	id := c.CollectLocalIdentity(context.Background())

	if id.Type != "local" {
		t.Errorf("expected type 'local', got %q", id.Type)
	}
	if id.OSUser != "testuser" {
		t.Errorf("expected os_user 'testuser', got %q", id.OSUser)
	}
	if id.UID != "1001" {
		t.Errorf("expected uid '1001', got %q", id.UID)
	}
	if id.Hostname != "dev-laptop.local" {
		t.Errorf("expected hostname 'dev-laptop.local', got %q", id.Hostname)
	}
	if id.CIProvider != "" {
		t.Errorf("expected empty ci_provider, got %q", id.CIProvider)
	}
}

func TestCollectLocalIdentity_GitHubActions(t *testing.T) {
	c := &Collector{
		Config: CollectConfig{OSUser: true, Hostname: true},
		Env: &mockEnvReader{vars: map[string]string{
			"GITHUB_ACTIONS":    "true",
			"GITHUB_RUN_ID":     "12345",
			"GITHUB_WORKFLOW":   "CI",
			"GITHUB_JOB":        "build",
			"GITHUB_SERVER_URL": "https://github.com",
			"GITHUB_REPOSITORY": "org/repo",
			"GITHUB_EVENT_NAME": "push",
			"GITHUB_ACTOR":      "octocat",
		}},
		User: &mockUserLookup{user: &user.User{Username: "runner", Uid: "1000"}},
		Host: &mockHostnameResolver{hostname: "runner-abc"},
	}

	id := c.CollectLocalIdentity(context.Background())

	if id.Type != "github-actions" {
		t.Errorf("expected type 'github-actions', got %q", id.Type)
	}
	if id.CIRunID != "12345" {
		t.Errorf("expected run_id '12345', got %q", id.CIRunID)
	}
	if id.CIPipeline != "CI" {
		t.Errorf("expected pipeline 'CI', got %q", id.CIPipeline)
	}
	if id.CIActor != "octocat" {
		t.Errorf("expected actor 'octocat', got %q", id.CIActor)
	}
	expectedURL := "https://github.com/org/repo/actions/runs/12345"
	if id.CIRunURL != expectedURL {
		t.Errorf("expected run_url %q, got %q", expectedURL, id.CIRunURL)
	}
}

func TestCollectLocalIdentity_PrivacyDisabled(t *testing.T) {
	c := &Collector{
		Config: CollectConfig{OSUser: false, Hostname: false, HomeDir: false},
		Env:    &mockEnvReader{vars: map[string]string{}},
		User: &mockUserLookup{user: &user.User{
			Username: "testuser",
			Uid:      "1001",
			HomeDir:  "/home/testuser",
		}},
		Host: &mockHostnameResolver{hostname: "secret-host"},
	}

	id := c.CollectLocalIdentity(context.Background())

	if id.OSUser != "" {
		t.Errorf("expected empty os_user when disabled, got %q", id.OSUser)
	}
	if id.Hostname != "" {
		t.Errorf("expected empty hostname when disabled, got %q", id.Hostname)
	}
	if id.HomeDir != "" {
		t.Errorf("expected empty home_dir when disabled, got %q", id.HomeDir)
	}
}

func TestCollectLocalIdentity_HashHostname(t *testing.T) {
	c := &Collector{
		Config: CollectConfig{Hostname: true, HashHostname: true},
		Env:    &mockEnvReader{vars: map[string]string{}},
		User:   &mockUserLookup{user: &user.User{}},
		Host:   &mockHostnameResolver{hostname: "my-laptop"},
	}

	id := c.CollectLocalIdentity(context.Background())

	expected := hashString("my-laptop")
	if id.Hostname != expected {
		t.Errorf("expected hashed hostname %q, got %q", expected, id.Hostname)
	}
}

func TestDetectCI_AllProviders(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		expected string
	}{
		{"github-actions", map[string]string{"GITHUB_ACTIONS": "true"}, "github-actions"},
		{"gitlab-ci", map[string]string{"GITLAB_CI": "true"}, "gitlab-ci"},
		{"jenkins", map[string]string{"JENKINS_URL": "http://jenkins"}, "jenkins"},
		{"circleci", map[string]string{"CIRCLECI": "true"}, "circleci"},
		{"azure-devops", map[string]string{"TF_BUILD": "True"}, "azure-devops"},
		{"bitbucket", map[string]string{"BITBUCKET_BUILD_NUMBER": "42"}, "bitbucket-pipelines"},
		{"teamcity", map[string]string{"TEAMCITY_VERSION": "2023.1"}, "teamcity"},
		{"codebuild", map[string]string{"CODEBUILD_BUILD_ID": "abc"}, "aws-codebuild"},
		{"spacelift", map[string]string{"SPACELIFT": "true"}, "spacelift"},
		{"atlantis", map[string]string{"ATLANTIS_TERRAFORM_VERSION": "1.5"}, "atlantis"},
		{"env0", map[string]string{"ENV0_ENVIRONMENT_ID": "xyz"}, "env0"},
		{"cloud-build", map[string]string{"BUILD_ID": "1", "PROJECT_ID": "proj"}, "google-cloud-build"},
		{"local", map[string]string{}, "local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Collector{
				Env:  &mockEnvReader{vars: tt.env},
				User: &mockUserLookup{user: &user.User{}},
				Host: &mockHostnameResolver{},
			}
			id := LocalIdentity{Type: "local"}
			c.detectCI(&id)
			if id.Type != tt.expected {
				t.Errorf("expected type %q, got %q", tt.expected, id.Type)
			}
		})
	}
}

func TestCollectGitFromRepo(t *testing.T) {
	repo := &mockGitRepo{
		hash:     "abc1234567890def",
		branch:   "main",
		detached: false,
		dirty:    true,
		remote:   "https://github.com/org/repo.git",
	}

	c := &Collector{
		Config: CollectConfig{},
		Env:    &mockEnvReader{vars: map[string]string{}},
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if !gc.Available {
		t.Error("expected available=true")
	}
	if gc.Commit != "abc1234567890def" {
		t.Errorf("expected full hash, got %q", gc.Commit)
	}
	if gc.Branch != "main" {
		t.Errorf("expected branch 'main', got %q", gc.Branch)
	}
	if !gc.Dirty {
		t.Error("expected dirty=true")
	}
	if gc.RemoteURL != "https://github.com/org/repo.git" {
		t.Errorf("expected clean remote URL, got %q", gc.RemoteURL)
	}
}

func TestCollectGitFromRepo_DetachedHead_ResolvesFromCI(t *testing.T) {
	repo := &mockGitRepo{
		hash:     "abc1234567890def",
		detached: true,
	}

	c := &Collector{
		Config: CollectConfig{},
		Env: &mockEnvReader{vars: map[string]string{
			"GITHUB_REF_NAME": "feature-branch",
		}},
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if gc.Branch != "feature-branch" {
		t.Errorf("expected branch 'feature-branch' from CI env, got %q", gc.Branch)
	}
}

func TestSanitizeRemoteURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// HTTPS with token
		{"https://token123@github.com/org/repo.git", "https://github.com/org/repo.git"},
		// HTTPS with user:pass
		{"https://user:pass@github.com/org/repo.git", "https://github.com/org/repo.git"},
		// SSH (no credentials to strip)
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		// Plain HTTPS (no credentials)
		{"https://github.com/org/repo.git", "https://github.com/org/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeRemoteURL(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeRemoteURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCollectGitFromRepo_TrackedBranch_OnMain(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	repo := &mockGitRepo{
		hash:   "abc1234567890def",
		branch: "main",
		remote: "https://github.com/org/repo.git",
		branchCommits: map[string]struct {
			info *CommitInfo
			hash string
		}{
			"main": {
				info: &CommitInfo{
					Author:    "Jane Doe",
					Email:     "jane@example.com",
					Message:   "Merge PR #42",
					Timestamp: ts,
				},
				hash: "abc1234567890def",
			},
		},
		isAncestorResult: true,
		commitsAhead:     0,
		commitsBehind:    0,
	}

	c := &Collector{
		Config:        CollectConfig{},
		Env:           &mockEnvReader{vars: map[string]string{}},
		TrackedBranch: "main",
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if gc.TrackedBranch != "main" {
		t.Errorf("expected tracked_branch 'main', got %q", gc.TrackedBranch)
	}
	if !gc.IsMerged {
		t.Error("expected is_merged=true when on main")
	}
	if !gc.IsCurrentBranch {
		t.Error("expected is_current_branch=true when on main")
	}
	if gc.CommitsAhead != 0 {
		t.Errorf("expected commits_ahead=0, got %d", gc.CommitsAhead)
	}
	if gc.TrackedCommitAuthor != "Jane Doe" {
		t.Errorf("expected tracked author 'Jane Doe', got %q", gc.TrackedCommitAuthor)
	}
}

func TestCollectGitFromRepo_TrackedBranch_FeatureBranch(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	repo := &mockGitRepo{
		hash:   "fff9999888877766",
		branch: "feat-xyz",
		remote: "https://github.com/org/repo.git",
		branchCommits: map[string]struct {
			info *CommitInfo
			hash string
		}{
			"main": {
				info: &CommitInfo{
					Author:    "Jane Doe",
					Email:     "jane@example.com",
					Message:   "Merge PR #42",
					Timestamp: ts,
				},
				hash: "abc1234567890def",
			},
		},
		isAncestorResult: false,
		commitsAhead:     3,
		commitsBehind:    0,
	}

	c := &Collector{
		Config:        CollectConfig{},
		Env:           &mockEnvReader{vars: map[string]string{}},
		TrackedBranch: "main",
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if gc.IsMerged {
		t.Error("expected is_merged=false on feature branch")
	}
	if gc.IsCurrentBranch {
		t.Error("expected is_current_branch=false on feature branch")
	}
	if gc.CommitsAhead != 3 {
		t.Errorf("expected commits_ahead=3, got %d", gc.CommitsAhead)
	}
	if gc.TrackedCommit != "abc1234567890def" {
		t.Errorf("expected tracked commit hash, got %q", gc.TrackedCommit)
	}
}

func TestCollectGitFromRepo_NoTrackedBranch(t *testing.T) {
	repo := &mockGitRepo{
		hash:   "abc1234567890def",
		branch: "main",
		remote: "https://github.com/org/repo.git",
	}

	c := &Collector{
		Config: CollectConfig{},
		Env:    &mockEnvReader{vars: map[string]string{}},
		// TrackedBranch not set
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if gc.TrackedBranch != "" {
		t.Errorf("expected empty tracked_branch when not configured, got %q", gc.TrackedBranch)
	}
	if gc.IsMerged {
		t.Error("expected is_merged=false when tracked branch not configured")
	}
}

func TestCollectGitFromRepo_RepoMismatch(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	repo := &mockGitRepo{
		hash:   "abc1234567890def",
		branch: "main",
		remote: "https://github.com/org/other-repo.git", // different repo
		branchCommits: map[string]struct {
			info *CommitInfo
			hash string
		}{
			"main": {
				info: &CommitInfo{
					Author:    "Jane Doe",
					Email:     "jane@example.com",
					Message:   "Merge PR #42",
					Timestamp: ts,
				},
				hash: "abc1234567890def",
			},
		},
		isAncestorResult: true,
		commitsAhead:     0,
		commitsBehind:    0,
	}

	c := &Collector{
		Config:        CollectConfig{},
		Env:           &mockEnvReader{vars: map[string]string{}},
		TrackedBranch: "main",
		TrackedRepo:   "https://github.com/org/infra",
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if !gc.RepoMismatch {
		t.Error("expected repo_mismatch=true when remote doesn't match tracked_repo")
	}
	if gc.TrackedBranch != "" {
		t.Errorf("expected empty tracked_branch on repo mismatch, got %q", gc.TrackedBranch)
	}
	if gc.IsMerged {
		t.Error("expected is_merged=false on repo mismatch")
	}
}

func TestCollectGitFromRepo_RepoMatch(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	repo := &mockGitRepo{
		hash:   "abc1234567890def",
		branch: "main",
		remote: "git@github.com:org/infra.git", // SSH variant of tracked_repo
		branchCommits: map[string]struct {
			info *CommitInfo
			hash string
		}{
			"main": {
				info: &CommitInfo{
					Author:    "Jane Doe",
					Email:     "jane@example.com",
					Message:   "Merge PR #42",
					Timestamp: ts,
				},
				hash: "abc1234567890def",
			},
		},
		isAncestorResult: true,
		commitsAhead:     0,
		commitsBehind:    0,
	}

	c := &Collector{
		Config:        CollectConfig{},
		Env:           &mockEnvReader{vars: map[string]string{}},
		TrackedBranch: "main",
		TrackedRepo:   "https://github.com/org/infra",
	}

	gc := c.collectGitFromRepo(context.Background(), repo)

	if gc.RepoMismatch {
		t.Error("expected repo_mismatch=false when SSH remote matches HTTPS tracked_repo")
	}
	if gc.TrackedBranch != "main" {
		t.Errorf("expected tracked_branch 'main', got %q", gc.TrackedBranch)
	}
	if !gc.IsMerged {
		t.Error("expected is_merged=true")
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://github.com/org/repo", "github.com/org/repo"},
		{"https://github.com/org/repo.git", "github.com/org/repo"},
		{"git@github.com:org/repo.git", "github.com/org/repo"},
		{"git@github.com:org/repo", "github.com/org/repo"},
		{"https://token@github.com/org/repo.git", "github.com/org/repo"},
		{"https://github.com/Org/Repo", "github.com/org/repo"},
	}

	for _, tt := range tests {
		got := normalizeRepoURL(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeRepoURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestHashString(t *testing.T) {
	h1 := hashString("test")
	h2 := hashString("test")
	h3 := hashString("other")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("SHA-256 hex should be 64 chars, got %d", len(h1))
	}
}
