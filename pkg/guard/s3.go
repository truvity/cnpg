package guard

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Lister is the ArchiveLister over the S3 API. It needs s3:ListBucket on
// the bucket and nothing else: it never reads an object.
type S3Lister struct {
	client *s3.Client
}

// S3Options configure NewS3Lister.
type S3Options struct {
	// Region of the bucket. Empty takes the SDK's own (AWS_REGION, the
	// profile), then us-east-1.
	Region string
	// Endpoint of an S3-compatible store (R2, MinIO, …), addressed
	// path-style. Empty is AWS.
	Endpoint string
}

// NewS3Lister builds an S3Lister from the SDK's default credential chain:
// the environment, then the container credentials EKS Pod Identity injects
// (AWS_CONTAINER_CREDENTIALS_FULL_URI), then web identity (IRSA), then the
// instance profile.
func NewS3Lister(ctx context.Context, opt S3Options) (*S3Lister, error) {
	var loads []func(*config.LoadOptions) error
	if opt.Region != "" {
		loads = append(loads, config.WithRegion(opt.Region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loads...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if opt.Endpoint != "" {
			o.BaseEndpoint = aws.String(opt.Endpoint)
			o.UsePathStyle = true
		}
	})

	return &S3Lister{client: client}, nil
}

// HasObjects implements ArchiveLister with one ListObjectsV2 of at most one key.
func (l *S3Lister) HasObjects(ctx context.Context, bucket, prefix string) (bool, error) {
	out, err := l.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, err
	}

	return len(out.Contents) > 0, nil
}
