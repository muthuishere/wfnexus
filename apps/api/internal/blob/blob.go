// Package blob wraps minio-go for artifact storage (MinIO locally, any S3-compatible in prod).
package blob

import (
	"context"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Blob struct {
	c      *minio.Client
	bucket string
}

func Open(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Blob, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}
	return &Blob{c: c, bucket: bucket}, nil
}

func (b *Blob) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := b.c.PutObject(ctx, b.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (b *Blob) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.c.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
}

func (b *Blob) PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := b.c.PresignedGetObject(ctx, b.bucket, key, ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
