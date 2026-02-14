package repository

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIOClient struct {
	client     *minio.Client
	bucketName string
}

func NewMinIOClient(endpoint, accessKey, secretKey, bucketName string) (*MinIOClient, error) {
	secure := getEnvBool("MINIO_SECURE", true)
	host := endpoint
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("minio endpoint parse: %w", err)
		}
		host = parsed.Host
		secure = parsed.Scheme == "https"
	}

	// Initialize minio client object.
	minioClient, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("minio connection: %w", err)
	}

	// Ensure bucket exists
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("minio bucket check: %w", err)
	}
	if !exists {
		err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			return nil, fmt.Errorf("minio make bucket: %w", err)
		}
		slog.Info("Created MinIO bucket", "bucket", bucketName)
	}

	return &MinIOClient{
		client:     minioClient,
		bucketName: bucketName,
	}, nil
}

// UploadFile uploads a file to MinIO
func (m *MinIOClient) UploadFile(ctx context.Context, objectName string, reader io.Reader, objectSize int64) error {
	_, err := m.client.PutObject(ctx, m.bucketName, objectName, reader, objectSize, minio.PutObjectOptions{
		ContentType: "application/zip",
	})
	if err != nil {
		return fmt.Errorf("minio upload: %w", err)
	}
	return nil
}

// RemoveFile removes a file from MinIO
func (m *MinIOClient) RemoveFile(ctx context.Context, objectName string) error {
	err := m.client.RemoveObject(ctx, m.bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("minio remove: %w", err)
	}
	return nil
}

// DownloadFile downloads a file to a local path
func (m *MinIOClient) DownloadFile(ctx context.Context, objectName string, localPath string) error {
	err := m.client.FGetObject(ctx, m.bucketName, objectName, localPath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("minio download: %w", err)
	}
	return nil
}
