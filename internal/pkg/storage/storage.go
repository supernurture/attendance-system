package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
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
	Headers   map[string]string // the client must send all of them, exactly
}

// PresignPut signs a direct client upload. size is signed too, which is what caps it, and the URL
// writes once, so a verified object cannot be replaced through it.
func (s *Storage) PresignPut(ctx context.Context, key, contentType string, size int64) (PresignedURL, error) {
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
		IfNoneMatch:   aws.String("*"),
	}, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return PresignedURL{}, fmt.Errorf("presign put %q: %w", key, err)
	}

	return PresignedURL{
		URL:       req.URL,
		ExpiresAt: time.Now().Add(s.ttl),
		Headers:   map[string]string{"Content-Type": contentType, "If-None-Match": "*"},
	}, nil
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

// ErrNotFound means the key holds no object.
var ErrNotFound = errors.New("object not found")

// Size returns the object's length in bytes, read from its headers; ErrNotFound if the key holds nothing.
func (s *Storage) Size(ctx context.Context, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, wrap("head", key, err)
	}
	return aws.ToInt64(out.ContentLength), nil
}

// ReadPrefix returns up to the first n bytes, fetching only those.
func (s *Storage) ReadPrefix(ctx context.Context, key string, n int) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", n-1)),
	})
	if err != nil {
		return nil, wrap("get", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	head, err := io.ReadAll(io.LimitReader(out.Body, int64(n)))
	if err != nil {
		return nil, wrap("read", key, err)
	}
	return head, nil
}

// Delete is not an error for a missing key.
func (s *Storage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return wrap("delete", key, err)
	}
	return nil
}

func wrap(op, key string, err error) error {
	if resp, ok := errors.AsType[*awshttp.ResponseError](err); ok && resp.HTTPStatusCode() == http.StatusNotFound {
		err = ErrNotFound
	}
	return fmt.Errorf("%s %q: %w", op, key, err)
}
