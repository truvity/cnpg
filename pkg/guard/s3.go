package guard

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Lister is the ArchiveLister over the S3 API. It needs s3:ListBucket on
// the bucket and nothing else: it never reads an object. The client is built
// on the first listing, so a check that never lists (the Cluster exists)
// needs neither a region nor credentials.
type S3Lister struct {
	opt    S3Options
	client *s3.Client
}

// S3Options configure NewS3Lister.
type S3Options struct {
	// Region of the bucket. Empty takes the SDK's own (AWS_REGION, the
	// profile); with neither, the first listing fails.
	Region string
	// Endpoint of an S3-compatible store (R2, MinIO, …), addressed
	// path-style. Empty is AWS.
	Endpoint string
}

// NewS3Lister returns an S3Lister that reads with the SDK's default
// credential chain: the environment, then the container credentials EKS Pod
// Identity injects (AWS_CONTAINER_CREDENTIALS_FULL_URI), then web identity
// (IRSA), then the instance profile.
func NewS3Lister(opt S3Options) *S3Lister {
	return &S3Lister{opt: opt}
}

func (l *S3Lister) s3Client(ctx context.Context) (*s3.Client, error) {
	if l.client != nil {
		return l.client, nil
	}

	var loads []func(*config.LoadOptions) error
	if l.opt.Region != "" {
		loads = append(loads, config.WithRegion(l.opt.Region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loads...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	// No built-in default: the bucket's region is an input. An
	// S3-compatible store that ignores it (R2, MinIO) takes any name.
	if cfg.Region == "" {
		return nil, errors.New("no region: pass --region (the bucket's) or set AWS_REGION")
	}

	l.client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		if l.opt.Endpoint != "" {
			o.BaseEndpoint = aws.String(l.opt.Endpoint)
			o.UsePathStyle = true
		}
	})

	return l.client, nil
}

// HasObjects implements ArchiveLister with one ListObjectsV2 of at most one key.
func (l *S3Lister) HasObjects(ctx context.Context, bucket, prefix string) (bool, error) {
	client, err := l.s3Client(ctx)
	if err != nil {
		return false, err
	}

	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, err
	}

	return len(out.Contents) > 0, nil
}
