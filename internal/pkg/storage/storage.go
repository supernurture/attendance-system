package storage

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config holds everything needed to reach one bucket.
type Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
	PresignTTL      time.Duration
}

// Storage is a client for a single bucket.
type Storage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	ttl     time.Duration
}

const headBucketTimeout = 5 * time.Second

// New builds the client and HeadBuckets it, so bad credentials fail at startup, not mid-request.
func New(cfg Config) (*Storage, error) {
	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),

		// R2 rejects the SDK's default CRC32 checksum.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = cfg.ForcePathStyle
	})

	ctx, cancel := context.WithTimeout(context.Background(), headBucketTimeout)
	defer cancel()
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		return nil, fmt.Errorf("reach bucket %q at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}

	log.Printf("connected to object storage bucket %q at %s\n", cfg.Bucket, cfg.Endpoint)
	return &Storage{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  cfg.Bucket,
		ttl:     cfg.PresignTTL,
	}, nil
}

// PresignedURL is a signed URL and the moment it stops working.
type PresignedURL struct {
	URL       string
	ExpiresAt time.Time
}

// PresignPut signs a direct client upload. size is signed too, which is what caps it.
func (s *Storage) PresignPut(ctx context.Context, key, contentType string, size int64) (PresignedURL, error) {
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return PresignedURL{}, fmt.Errorf("presign put %q: %w", key, err)
	}

	return PresignedURL{URL: req.URL, ExpiresAt: time.Now().Add(s.ttl)}, nil
}

// PresignGet signs a direct client download. Anyone holding the URL can read it until it expires.
func (s *Storage) PresignGet(ctx context.Context, key string) (PresignedURL, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return PresignedURL{}, fmt.Errorf("presign get %q: %w", key, err)
	}

	return PresignedURL{URL: req.URL, ExpiresAt: time.Now().Add(s.ttl)}, nil
}
