package collectors

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// ciProviderDef defines how to detect and extract info from a CI system.
type ciProviderDef struct {
	Name      string
	DetectEnv string
	DetectVal string // if empty, just check env var existence
	Fields    func(env EnvReader) ciFields
}

type ciFields struct {
	RunID    string
	Pipeline string
	Job      string
	RunURL   string
	Trigger  string
	Actor    string
}

// ciProviders is the ordered list of CI systems to detect. First match wins.
var ciProviders = []ciProviderDef{
	{
		Name: "github-actions", DetectEnv: "GITHUB_ACTIONS", DetectVal: "true",
		Fields: func(env EnvReader) ciFields {
			serverURL := env.Getenv("GITHUB_SERVER_URL")
			repo := env.Getenv("GITHUB_REPOSITORY")
			runID := env.Getenv("GITHUB_RUN_ID")
			var runURL string
			if serverURL != "" && repo != "" && runID != "" {
				runURL = serverURL + "/" + repo + "/actions/runs/" + runID
			}
			return ciFields{
				RunID:    runID,
				Pipeline: env.Getenv("GITHUB_WORKFLOW"),
				Job:      env.Getenv("GITHUB_JOB"),
				RunURL:   runURL,
				Trigger:  env.Getenv("GITHUB_EVENT_NAME"),
				Actor:    env.Getenv("GITHUB_ACTOR"),
			}
		},
	},
	{
		Name: "gitlab-ci", DetectEnv: "GITLAB_CI", DetectVal: "true",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("CI_PIPELINE_ID"),
				Pipeline: env.Getenv("CI_PIPELINE_NAME"),
				Job:      env.Getenv("CI_JOB_NAME"),
				RunURL:   env.Getenv("CI_PIPELINE_URL"),
				Trigger:  env.Getenv("CI_PIPELINE_SOURCE"),
				Actor:    env.Getenv("GITLAB_USER_LOGIN"),
			}
		},
	},
	{
		Name: "jenkins", DetectEnv: "JENKINS_URL",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("BUILD_ID"),
				Pipeline: env.Getenv("JOB_NAME"),
				Job:      env.Getenv("BUILD_TAG"),
				RunURL:   env.Getenv("BUILD_URL"),
				Trigger:  env.Getenv("BUILD_CAUSE"),
				Actor:    env.Getenv("BUILD_USER"),
			}
		},
	},
	{
		Name: "circleci", DetectEnv: "CIRCLECI", DetectVal: "true",
		Fields: func(env EnvReader) ciFields {
			trigger := ""
			if env.Getenv("CIRCLE_TAG") != "" {
				trigger = "tag"
			} else if env.Getenv("CIRCLE_BRANCH") != "" {
				trigger = "push"
			}
			return ciFields{
				RunID:    env.Getenv("CIRCLE_BUILD_NUM"),
				Pipeline: env.Getenv("CIRCLE_PROJECT_REPONAME"),
				Job:      env.Getenv("CIRCLE_JOB"),
				RunURL:   env.Getenv("CIRCLE_BUILD_URL"),
				Trigger:  trigger,
				Actor:    env.Getenv("CIRCLE_USERNAME"),
			}
		},
	},
	{
		Name: "azure-devops", DetectEnv: "TF_BUILD", DetectVal: "True",
		Fields: func(env EnvReader) ciFields {
			collectionURI := env.Getenv("SYSTEM_TEAMFOUNDATIONCOLLECTIONURI")
			project := env.Getenv("SYSTEM_TEAMPROJECT")
			buildID := env.Getenv("BUILD_BUILDID")
			var runURL string
			if collectionURI != "" && project != "" && buildID != "" {
				runURL = collectionURI + project + "/_build/results?buildId=" + buildID
			}
			return ciFields{
				RunID:    buildID,
				Pipeline: env.Getenv("BUILD_DEFINITIONNAME"),
				Job:      env.Getenv("AGENT_JOBNAME"),
				RunURL:   runURL,
				Trigger:  env.Getenv("BUILD_REASON"),
				Actor:    env.Getenv("BUILD_REQUESTEDFOR"),
			}
		},
	},
	{
		Name: "bitbucket-pipelines", DetectEnv: "BITBUCKET_BUILD_NUMBER",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("BITBUCKET_BUILD_NUMBER"),
				Pipeline: env.Getenv("BITBUCKET_REPO_SLUG"),
				Job:      env.Getenv("BITBUCKET_STEP_UUID"),
				Trigger:  env.Getenv("BITBUCKET_PIPELINE_TRIGGER"),
				Actor:    env.Getenv("BITBUCKET_STEP_TRIGGERER_UUID"),
			}
		},
	},
	{
		Name: "teamcity", DetectEnv: "TEAMCITY_VERSION",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("BUILD_NUMBER"),
				Pipeline: env.Getenv("TEAMCITY_BUILDCONF_NAME"),
			}
		},
	},
	{
		Name: "aws-codebuild", DetectEnv: "CODEBUILD_BUILD_ID",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("CODEBUILD_BUILD_ID"),
				Pipeline: env.Getenv("CODEBUILD_BUILD_ARN"),
				Actor:    env.Getenv("CODEBUILD_INITIATOR"),
			}
		},
	},
	{
		Name: "spacelift", DetectEnv: "SPACELIFT", DetectVal: "true",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("TF_VAR_spacelift_run_id"),
				Pipeline: env.Getenv("TF_VAR_spacelift_stack_id"),
			}
		},
	},
	{
		Name: "atlantis", DetectEnv: "ATLANTIS_TERRAFORM_VERSION",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				Pipeline: env.Getenv("HEAD_REPO_NAME"),
				Actor:    env.Getenv("PULL_AUTHOR"),
			}
		},
	},
	{
		Name: "env0", DetectEnv: "ENV0_ENVIRONMENT_ID",
		Fields: func(env EnvReader) ciFields {
			return ciFields{
				RunID:    env.Getenv("ENV0_DEPLOYMENT_ID"),
				Pipeline: env.Getenv("ENV0_ENVIRONMENT_NAME"),
			}
		},
	},
}

// CollectLocalIdentity gathers OS user info, hostname, and CI environment detection.
func (c *Collector) CollectLocalIdentity(ctx context.Context) LocalIdentity {
	id := LocalIdentity{
		Type: "local",
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		PID:  os.Getpid(),
		PPID: os.Getppid(),
	}

	// OS user
	if c.Config.OSUser {
		c.collectOSUser(&id)
	}

	// Hostname
	if c.Config.Hostname {
		c.collectHostname(ctx, &id)
	}

	// Home directory
	if c.Config.HomeDir {
		c.collectHomeDir(ctx, &id)
	}

	// CI detection
	c.detectCI(&id)

	return id
}

func (c *Collector) collectOSUser(id *LocalIdentity) {
	u, err := c.User.Current()
	if err != nil {
		// Fallback to env vars
		if runtime.GOOS == "windows" {
			id.OSUser = c.Env.Getenv("USERNAME")
		} else {
			id.OSUser = c.Env.Getenv("USER")
		}
		return
	}

	id.OSUser = u.Username
	id.UID = u.Uid

	// Normalize Windows DOMAIN\username if configured
	if c.Config.NormalizeUser && runtime.GOOS == "windows" {
		if _, after, found := strings.Cut(id.OSUser, `\`); found {
			id.OSUser = after
		}
	}
}

func (c *Collector) collectHostname(ctx context.Context, id *LocalIdentity) {
	hostname, err := c.Host.Hostname()
	if err != nil {
		tflog.Debug(ctx, "failed to get hostname", map[string]interface{}{"error": err.Error()})
		return
	}

	if c.Config.HashHostname {
		id.Hostname = hashString(hostname)
	} else {
		id.Hostname = hostname
	}
}

func (c *Collector) collectHomeDir(ctx context.Context, id *LocalIdentity) {
	u, err := c.User.Current()
	if err != nil {
		tflog.Debug(ctx, "failed to get home dir", map[string]interface{}{"error": err.Error()})
		return
	}
	id.HomeDir = u.HomeDir
}

func (c *Collector) detectCI(id *LocalIdentity) {
	for _, ci := range ciProviders {
		val := c.Env.Getenv(ci.DetectEnv)
		if val == "" {
			continue
		}
		if ci.DetectVal != "" && val != ci.DetectVal {
			continue
		}

		// Matched a CI provider
		id.Type = ci.Name
		id.CIProvider = ci.Name

		if ci.Fields != nil {
			fields := ci.Fields(c.Env)
			id.CIRunID = fields.RunID
			id.CIPipeline = fields.Pipeline
			id.CIJob = fields.Job
			id.CIRunURL = fields.RunURL
			id.CITrigger = fields.Trigger
			id.CIActor = fields.Actor
		}
		return
	}

	// Special case: Google Cloud Build needs two env vars
	if c.Env.Getenv("BUILD_ID") != "" && c.Env.Getenv("PROJECT_ID") != "" {
		id.Type = "google-cloud-build"
		id.CIProvider = "google-cloud-build"
		id.CIRunID = c.Env.Getenv("BUILD_ID")
		return
	}
}

// hashString returns the hex-encoded SHA-256 hash of s.
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
