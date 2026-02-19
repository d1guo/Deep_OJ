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
			return nil, fmt.Errorf("解析 MinIO 端点失败: %w", err)
		}
		host = parsed.Host
		secure = parsed.Scheme == "https"
	}

	// 初始化 MinIO 客户端。
	minioClient, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("连接 MinIO 失败: %w", err)
	}

	// 确保存储桶存在。
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("检查 MinIO 桶失败: %w", err)
	}
	if !exists {
		err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			return nil, fmt.Errorf("创建 MinIO 桶失败: %w", err)
		}
		slog.Info("已创建 MinIO 桶", "bucket", bucketName)
	}

	return &MinIOClient{
		client:     minioClient,
		bucketName: bucketName,
	}, nil
}

// UploadFile 上传文件到 MinIO。
func (m *MinIOClient) UploadFile(ctx context.Context, objectName string, reader io.Reader, objectSize int64) error {
	_, err := m.client.PutObject(ctx, m.bucketName, objectName, reader, objectSize, minio.PutObjectOptions{
		ContentType: "application/zip",
	})
	if err != nil {
		return fmt.Errorf("MinIO 上传失败: %w", err)
	}
	return nil
}

// RemoveFile 从 MinIO 删除文件。
func (m *MinIOClient) RemoveFile(ctx context.Context, objectName string) error {
	err := m.client.RemoveObject(ctx, m.bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("MinIO 删除失败: %w", err)
	}
	return nil
}

// DownloadFile 从 MinIO 下载文件到本地路径。
func (m *MinIOClient) DownloadFile(ctx context.Context, objectName string, localPath string) error {
	err := m.client.FGetObject(ctx, m.bucketName, objectName, localPath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("MinIO 下载失败: %w", err)
	}
	return nil
}
