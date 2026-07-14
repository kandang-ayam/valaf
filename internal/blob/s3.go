package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3Store is any S3-compatible backend (AWS S3, MinIO, Ceph). The bucket must
// already exist (or be creatable by the credentials).
type s3Store struct {
	client *minio.Client
	bucket string
}

func newS3(cfg Config) (Store, error) {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.S3Endpoint, "https://"), "http://")
	useSSL := cfg.S3UseSSL || strings.HasPrefix(cfg.S3Endpoint, "https://")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}

	// Ensure the bucket exists (best-effort; ignore "already owned").
	ctx := context.Background()
	if ok, err := client.BucketExists(ctx, cfg.S3Bucket); err == nil && !ok {
		if err := client.MakeBucket(ctx, cfg.S3Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket %q: %w", cfg.S3Bucket, err)
		}
	}
	return &s3Store{client: client, bucket: cfg.S3Bucket}, nil
}

func (s *s3Store) Backend() string { return "s3" }

func (s *s3Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *s3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
