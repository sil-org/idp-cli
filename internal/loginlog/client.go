package loginlog

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// ClientOptions configures NewClient. All fields are optional.
type ClientOptions struct {
	// Region overrides the region from the environment or profile.
	Region string

	// Profile selects a named profile from the shared AWS config and
	// credentials files (~/.aws/config, ~/.aws/credentials), like the AWS
	// CLI's --profile. Empty means the default chain, which still honors
	// AWS_PROFILE.
	Profile string

	// Endpoint is only for dynamodb-local, e.g. http://localhost:8000.
	Endpoint string
}

// NewClient builds a DynamoDB client from the standard AWS credential chain
// (env vars, shared config/profile, SSO, instance role).
func NewClient(ctx context.Context, opts ClientOptions) (*dynamodb.Client, error) {
	var loadOpts []func(*config.LoadOptions) error
	if opts.Region != "" {
		loadOpts = append(loadOpts, config.WithRegion(opts.Region))
	}
	if opts.Profile != "" {
		loadOpts = append(loadOpts, config.WithSharedConfigProfile(opts.Profile))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("no AWS region set; use --region, a --profile that sets one, or AWS_REGION")
	}

	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if opts.Endpoint != "" {
			o.BaseEndpoint = aws.String(opts.Endpoint)
		}
	}), nil
}
