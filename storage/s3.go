package storage

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var FilesBucket S3Bucket

type S3Bucket struct {
	S3   *s3.Client
	Name string
}

func newBucket(client *s3.Client, name string) S3Bucket {
	return S3Bucket{
		S3:   client,
		Name: name,
	}
}

func (b S3Bucket) Put(ctx context.Context, contentType, key string, r io.ReadSeeker) error {
	_, err := b.S3.PutObject(ctx, &s3.PutObjectInput{
		Body:        r,
		Bucket:      aws.String(b.Name),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	})
	return err
}

func (b S3Bucket) Delete(ctx context.Context, key string) error {
	_, err := b.S3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.Name),
		Key:    aws.String(key),
	})
	return err
}

func (b S3Bucket) PutFile(ctx context.Context, key string, f *os.File, size int64) error {
	_, err := b.S3.PutObject(ctx, &s3.PutObjectInput{
		Body:          f,
		Bucket:        aws.String(b.Name),
		Key:           aws.String(key),
		ContentType:   aws.String("application/octet-stream"),
		ContentLength: aws.Int64(size),
	})
	return err
}

func (b S3Bucket) GetRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	out, err := b.S3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.Name),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", off, off+n-1)),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(io.LimitReader(out.Body, n))
}

func newClient(region, endpoint, keyID, secret string, pathStyle bool) *s3.Client {
	cfg := aws.Config{
		Region:                     region,
		Credentials:                credentials.NewStaticCredentialsProvider(keyID, secret, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = pathStyle
	})
}

type Config struct {
	Type StorageType

	FilesBucket string

	Region   string
	Endpoint string

	AccessKeyID     string
	AccessKeySecret string

	CFAccountID string
}

type StorageType string

const (
	StorageTypeS3 StorageType = "s3"
	StorageTypeB2 StorageType = "b2"
	StorageTypeR2 StorageType = "r2"
)

func clientFor(cfg Config) (*s3.Client, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, fmt.Errorf("missing storage.access_key_id or storage.access_key_secret in configuration")
	}
	switch cfg.Type {
	case StorageTypeS3:
		return newClient(cfg.Region, cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, cfg.Endpoint != ""), nil
	case StorageTypeB2:
		endpoint := fmt.Sprintf("https://s3.%s.backblazeb2.com", cfg.Region)
		return newClient(cfg.Region, endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, true), nil
	case StorageTypeR2:
		endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.CFAccountID)
		return newClient("auto", endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, false), nil
	case "":
		return nil, fmt.Errorf("missing storage.type in configuration")
	default:
		return nil, fmt.Errorf("unknown storage.type in configuration: %q", cfg.Type)
	}
}

func Init(cfg Config) error {
	client, err := clientFor(cfg)
	if err != nil {
		return err
	}

	FilesBucket = newBucket(client, cfg.FilesBucket)
	return nil
}
