package collectors

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

// CollectionResult holds all collected context from a Terraform run.
type CollectionResult struct {
	Identity    LocalIdentity `json:"identity"`
	Git         GitContext    `json:"git"`
	CollectedAt time.Time     `json:"collected_at"`
}

// LocalIdentity identifies the human or system account initiating the Terraform run.
type LocalIdentity struct {
	Type string `json:"type"` // "local" or CI provider name

	// OS-level identity
	OSUser   string `json:"os_user"`
	UID      string `json:"uid"`
	Hostname string `json:"hostname"`
	HomeDir  string `json:"home_dir,omitempty"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	PID      int    `json:"pid"`
	PPID     int    `json:"ppid"`

	// CI-specific (populated when Type != "local")
	CIProvider string `json:"ci_provider,omitempty"`
	CIRunID    string `json:"ci_run_id,omitempty"`
	CIPipeline string `json:"ci_pipeline,omitempty"`
	CIJob      string `json:"ci_job,omitempty"`
	CIRunURL   string `json:"ci_run_url,omitempty"`
	CITrigger  string `json:"ci_trigger,omitempty"`
	CIActor    string `json:"ci_actor,omitempty"`
}

// GitContext captures compliance-focused git provenance.
// Minimal HEAD info + tracked branch compliance data.
type GitContext struct {
	Available   bool   `json:"available"`
	Commit      string `json:"commit"`                 // HEAD commit hash
	Branch      string `json:"branch"`                 // current branch name
	Dirty       bool   `json:"dirty"`                  // uncommitted changes?
	RemoteURL   string `json:"remote_url"`             // origin remote URL (sanitized, auto-detected)
	TrackedRepo string `json:"tracked_repo,omitempty"` // user-configured repository URL

	// Tracked branch compliance (only populated when tracked_branch is configured)
	TrackedBranch       string    `json:"tracked_branch,omitempty"` // configured primary branch name
	TrackedCommit       string    `json:"tracked_commit,omitempty"` // latest commit on tracked branch
	TrackedCommitShort  string    `json:"tracked_commit_short,omitempty"`
	TrackedCommitAuthor string    `json:"tracked_commit_author,omitempty"`
	TrackedCommitEmail  string    `json:"tracked_commit_email,omitempty"`
	TrackedCommitMsg    string    `json:"tracked_commit_message,omitempty"`
	TrackedCommitTime   time.Time `json:"tracked_commit_timestamp,omitempty"`
	RepoMismatch        bool      `json:"repo_mismatch"`     // detected remote != configured tracked_repo
	IsMerged            bool      `json:"is_merged"`         // is HEAD ancestor of tracked branch?
	IsCurrentBranch     bool      `json:"is_current_branch"` // current branch == tracked branch?
	CommitsAhead        int       `json:"commits_ahead"`     // HEAD commits not in tracked
	CommitsBehind       int       `json:"commits_behind"`    // tracked commits not in HEAD
}

// CollectConfig controls which fields are collected (privacy toggles).
type CollectConfig struct {
	OSUser        bool
	Hostname      bool
	HomeDir       bool
	PublicIP      bool
	CloudMetadata bool
	HashHostname  bool
	NormalizeUser bool
}

// DefaultCollectConfig returns default privacy settings.
func DefaultCollectConfig() CollectConfig {
	return CollectConfig{
		OSUser:   true,
		Hostname: true,
		HomeDir:  false,
	}
}

// --- Dependency interfaces (for testing) ---

// EnvReader reads environment variables.
type EnvReader interface {
	Getenv(key string) string
}

// UserLookup retrieves OS user information.
type UserLookup interface {
	Current() (*user.User, error)
}

// CommandRunner executes shell commands.
type CommandRunner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// FileReader reads files from disk.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	Stat(path string) (os.FileInfo, error)
}

// HostnameResolver resolves the machine hostname.
type HostnameResolver interface {
	Hostname() (string, error)
}

// GitOpener opens a Git repository at a given directory.
type GitOpener interface {
	Open(dir string) (GitRepo, error)
}

// GitRepo provides read-only access to a Git repository.
type GitRepo interface {
	HeadHash() (string, error)
	HeadBranch() (branch string, detached bool, err error)
	IsDirty() (bool, error)
	RemoteURL(name string) (string, error)
	BranchCommit(branch string) (*CommitInfo, string, error)
	IsAncestor(commitHash, branchRef string) (bool, error)
	CommitCounts(refA, refB string) (ahead int, behind int, err error)
	Close()
}

// CommitInfo holds metadata about a single Git commit.
type CommitInfo struct {
	Author    string
	Email     string
	Message   string
	Timestamp time.Time
}

// --- Default implementations ---

type osEnvReader struct{}

func (osEnvReader) Getenv(key string) string { return os.Getenv(key) }

type osUserLookup struct{}

func (osUserLookup) Current() (*user.User, error) { return user.Current() }

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

type osFileReader struct{}

func (osFileReader) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (osFileReader) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

type osHostnameResolver struct{}

func (osHostnameResolver) Hostname() (string, error) { return os.Hostname() }

// --- Collector ---

// Collector orchestrates all context collection with injectable dependencies.
type Collector struct {
	Config        CollectConfig
	Env           EnvReader
	User          UserLookup
	Cmd           CommandRunner
	FS            FileReader
	Host          HostnameResolver
	GitOp         GitOpener
	TrackedBranch string
	TrackedRepo   string
}

// NewCollector creates a Collector with real OS dependencies.
func NewCollector(config CollectConfig) *Collector {
	return &Collector{
		Config: config,
		Env:    osEnvReader{},
		User:   osUserLookup{},
		Cmd:    execCommandRunner{},
		FS:     osFileReader{},
		Host: osHostnameResolver{},
	}
}

// Collect runs all collectors and returns the combined result.
// Total timeout is 5 seconds. Individual cloud collectors get 3-second timeouts.
func (c *Collector) Collect(ctx context.Context) *CollectionResult {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return &CollectionResult{
		Identity:    c.CollectLocalIdentity(ctx),
		Git:         c.CollectGitContext(ctx),
		CollectedAt: time.Now().UTC(),
	}
}
