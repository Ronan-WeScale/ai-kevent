package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"kevent/gateway/internal/config"
)

// S3Client wraps the AWS SDK v2 S3 client for any S3-compatible object storage.
// It is configured via S3Config which carries the provider-specific endpoint and credentials
// (e.g. Scaleway: https://s3.fr-par.scw.cloud, region fr-par).
type S3Client struct {
	s3     *s3.Client
	bucket string
}

// NewS3Client builds a standard S3 client from the provided config.
// The BaseEndpoint field makes it compatible with any S3-compatible provider.
func NewS3Client(cfg config.S3Config) (*S3Client, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3: access_key and secret_key are required")
	}

	s3Client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       cfg.Region,
		Credentials: aws.NewCredentialsCache(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
		UsePathStyle: true,
	})

	return &S3Client{
		s3:     s3Client,
		bucket: cfg.Bucket,
	}, nil
}

// Upload stores a file stream as objectKey in the configured bucket.
// size is the content length in bytes; pass -1 if unknown (the SDK will
// attempt to seek the reader to determine the length automatically).
func (c *S3Client) Upload(ctx context.Context, objectKey string, reader io.Reader, size int64, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(objectKey),
		Body:        reader,
		ContentType: aws.String(contentType),
	}

	// Providing ContentLength avoids an extra seek round-trip and is required
	// when the body is a plain io.Reader that does not implement io.ReadSeeker.
	if size >= 0 {
		input.ContentLength = aws.Int64(size)
	}

	if _, err := c.s3.PutObject(ctx, input); err != nil {
		return fmt.Errorf("uploading %q to S3 bucket %q: %w", objectKey, c.bucket, err)
	}
	return nil
}

// GetObject downloads an object and returns its content as bytes.
func (c *S3Client) GetObject(ctx context.Context, objectKey string) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, fmt.Errorf("getting S3 object %q: %w", objectKey, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("reading S3 object %q: %w", objectKey, err)
	}
	return data, nil
}

// DeleteObject removes an object from the configured bucket.
func (c *S3Client) DeleteObject(ctx context.Context, objectKey string) error {
	if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(objectKey),
	}); err != nil {
		return fmt.Errorf("deleting S3 object %q: %w", objectKey, err)
	}
	return nil
}
