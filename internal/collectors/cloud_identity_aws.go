package collectors

import (
	"context"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// collectAWS detects AWS credentials and calls STS GetCallerIdentity.
func (c *Collector) collectAWS(ctx context.Context) *CloudIdentity {
	if !c.hasAWSCredentials() {
		return nil
	}

	stsCaller := c.STS
	if stsCaller == nil {
		var err error
		stsCaller, err = newRealSTSCaller(ctx)
		if err != nil {
			tflog.Warn(ctx, "failed to create AWS STS client", map[string]interface{}{"error": err.Error()})
			return nil
		}
	}

	output, err := stsCaller.GetCallerIdentity(ctx)
	if err != nil {
		tflog.Warn(ctx, "AWS GetCallerIdentity failed", map[string]interface{}{"error": err.Error()})
		// Return partial data from env vars if available
		accountID := c.Env.Getenv("AWS_ACCOUNT_ID")
		if accountID == "" {
			return nil
		}
		return &CloudIdentity{
			Provider:  "aws",
			AccountID: accountID,
			AuthType:  "unknown",
			AWS:       &AWSIdentity{AccountID: accountID, RoleType: "unknown"},
		}
	}

	awsID := parseAWSARN(output.ARN)
	awsID.AccountID = output.Account
	awsID.UserID = output.UserID

	return &CloudIdentity{
		Provider:    "aws",
		AccountID:   output.Account,
		PrincipalID: output.ARN,
		AuthType:    awsID.RoleType,
		AWS:         awsID,
	}
}

// hasAWSCredentials checks if AWS credentials are available via env vars or credential files.
func (c *Collector) hasAWSCredentials() bool {
	// Check env var credentials
	if c.Env.Getenv("AWS_ACCESS_KEY_ID") != "" {
		return true
	}
	if c.Env.Getenv("AWS_PROFILE") != "" {
		return true
	}
	// ECS container credentials
	if c.Env.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" {
		return true
	}
	// Web identity token (EKS IRSA)
	if c.Env.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" {
		return true
	}
	// Check default credentials file
	home, err := os.UserHomeDir()
	if err == nil {
		if _, err := c.FS.Stat(home + "/.aws/credentials"); err == nil {
			return true
		}
	}
	return false
}

// parseAWSARN parses an AWS ARN into its identity components.
//
// ARN formats:
//
//	arn:aws:iam::123456789012:user/username              → RoleType "user"
//	arn:aws:sts::123456789012:assumed-role/Role/Session  → RoleType "assumed-role"
//	arn:aws:sts::123456789012:federated-user/username    → RoleType "federated-user"
//	arn:aws:iam::123456789012:root                       → RoleType "root"
func parseAWSARN(arn string) *AWSIdentity {
	id := &AWSIdentity{ARN: arn}

	// arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		id.RoleType = "unknown"
		return id
	}

	resource := parts[5]

	switch {
	case resource == "root":
		id.RoleType = "root"

	case strings.HasPrefix(resource, "user/"):
		id.RoleType = "user"

	case strings.HasPrefix(resource, "assumed-role/"):
		id.RoleType = "assumed-role"
		// assumed-role/RoleName/SessionName
		roleParts := strings.SplitN(resource, "/", 3)
		if len(roleParts) >= 2 {
			id.RoleARN = "arn:" + parts[1] + ":iam::" + parts[4] + ":role/" + roleParts[1]
		}
		if len(roleParts) >= 3 {
			id.SessionName = roleParts[2]
		}

	case strings.HasPrefix(resource, "federated-user/"):
		id.RoleType = "federated-user"

	default:
		id.RoleType = "unknown"
	}

	return id
}

// --- Real STS implementation ---

type realSTSCaller struct {
	client *sts.Client
}

func newRealSTSCaller(ctx context.Context) (*realSTSCaller, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &realSTSCaller{client: sts.NewFromConfig(cfg)}, nil
}

func (s *realSTSCaller) GetCallerIdentity(ctx context.Context) (*STSOutput, error) {
	out, err := s.client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	return &STSOutput{
		ARN:     aws.ToString(out.Arn),
		Account: aws.ToString(out.Account),
		UserID:  aws.ToString(out.UserId),
	}, nil
}
