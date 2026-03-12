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
	Identity    LocalIdentity   `json:"identity"`
	Git         GitContext       `json:"git"`
	Cloud       []CloudIdentity `json:"cloud,omitempty"`
	CollectedAt time.Time       `json:"collected_at"`
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

// GitContext captures code provenance from the Git repository.
type GitContext struct {
	Available       bool      `json:"available"`
	Commit          string    `json:"commit"`
	CommitShort     string    `json:"commit_short"`
	Branch          string    `json:"branch"`
	Dirty           bool      `json:"dirty"`
	CommitAuthor    string    `json:"commit_author"`
	CommitEmail     string    `json:"commit_email"`
	CommitMessage   string    `json:"commit_message"`
	CommitTimestamp time.Time `json:"commit_timestamp"`
	LocalGitName    string    `json:"local_git_name,omitempty"`
	LocalGitEmail   string    `json:"local_git_email,omitempty"`
	RemoteURL       string    `json:"remote_url"`
	PRNumber        string    `json:"pr_number,omitempty"`
	Tags            []string  `json:"tags,omitempty"`
}

// CloudIdentity represents the normalized identity of a cloud caller.
type CloudIdentity struct {
	Provider    string         `json:"provider"`
	AccountID   string         `json:"account_id"`
	PrincipalID string         `json:"principal_id"`
	AuthType    string         `json:"auth_type"`
	DisplayName string         `json:"display_name,omitempty"`
	AWS         *AWSIdentity   `json:"aws,omitempty"`
	Azure       *AzureIdentity `json:"azure,omitempty"`
	GCP         *GCPIdentity   `json:"gcp,omitempty"`
}

// AWSIdentity holds AWS-specific caller identity from STS GetCallerIdentity.
type AWSIdentity struct {
	ARN         string `json:"arn"`
	AccountID   string `json:"account_id"`
	UserID      string `json:"user_id"`
	RoleARN     string `json:"role_arn,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	RoleType    string `json:"role_type"` // "user" | "assumed-role" | "federated-user" | "root"
}

// AzureIdentity holds Azure-specific caller identity from JWT token claims.
type AzureIdentity struct {
	TenantID       string `json:"tenant_id"`
	ObjectID       string `json:"object_id"`
	ClientID       string `json:"client_id,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	UPN            string `json:"upn,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	AuthType       string `json:"auth_type"` // "service-principal" | "managed-identity" | "user-cli" | "workload-identity"
}

// GCPIdentity holds GCP-specific caller identity from credential file or tokeninfo.
type GCPIdentity struct {
	ProjectID         string `json:"project_id"`
	ClientEmail       string `json:"client_email"`
	ClientID          string `json:"client_id,omitempty"`
	KeyID             string `json:"key_id,omitempty"`
	AuthType          string `json:"auth_type"` // "service-account" | "user-adc" | "workload-identity" | "gce-metadata"
	ImpersonatedEmail string `json:"impersonated_email,omitempty"`
}

// CollectConfig controls which fields are collected (privacy toggles).
type CollectConfig struct {
	OSUser        bool
	Hostname      bool
	HomeDir       bool
	GitIdentity   bool
	PublicIP      bool
	CloudMetadata bool
	HashHostname  bool
	NormalizeUser bool
}

// DefaultCollectConfig returns default privacy settings.
func DefaultCollectConfig() CollectConfig {
	return CollectConfig{
		OSUser:      true,
		Hostname:    true,
		HomeDir:     false,
		GitIdentity: true,
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

// STSCaller calls AWS STS GetCallerIdentity.
type STSCaller interface {
	GetCallerIdentity(ctx context.Context) (*STSOutput, error)
}

// STSOutput holds the raw STS GetCallerIdentity response fields.
type STSOutput struct {
	ARN     string
	Account string
	UserID  string
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
	HeadCommit() (*CommitInfo, error)
	RemoteURL(name string) (string, error)
	TagsAtHead() ([]string, error)
	ConfigValue(section, subsection, key string) (string, error)
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

func (osFileReader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (osFileReader) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

type osHostnameResolver struct{}

func (osHostnameResolver) Hostname() (string, error) { return os.Hostname() }

// --- Collector ---

// Collector orchestrates all context collection with injectable dependencies.
type Collector struct {
	Config CollectConfig
	Env    EnvReader
	User   UserLookup
	Cmd    CommandRunner
	FS     FileReader
	Host   HostnameResolver
	GitOp  GitOpener
	STS    STSCaller
}

// NewCollector creates a Collector with real OS dependencies.
func NewCollector(config CollectConfig) *Collector {
	return &Collector{
		Config: config,
		Env:    osEnvReader{},
		User:   osUserLookup{},
		Cmd:    execCommandRunner{},
		FS:     osFileReader{},
		Host:   osHostnameResolver{},
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
		Cloud:       c.CollectCloudIdentities(ctx),
		CollectedAt: time.Now().UTC(),
	}
}
