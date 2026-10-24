package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var (
	ReplicaBucket S3Bucket
	replicaLock   time.Duration
	replicaClass  string
	replicaDelay  time.Duration
)

const GlacierClass = "GLACIER"

type ReplicaOptions struct {
	LockDays     int
	StorageClass string
	DelayDays    int
}

var ErrNoObject = errors.New("storage: object not found")

func InitReplica(cfg Config, opts ReplicaOptions) error {
	client, err := clientFor(cfg)
	if err != nil {
		return err
	}
	ReplicaBucket = newBucket(client, cfg.FilesBucket)
	replicaLock = time.Duration(opts.LockDays) * 24 * time.Hour
	replicaClass = opts.StorageClass
	replicaDelay = time.Duration(opts.DelayDays) * 24 * time.Hour
	return nil
}

func ReplicaClass() string {
	return replicaClass
}

func ReplicaDelay() time.Duration {
	return replicaDelay
}

func Replicated() bool {
	return ReplicaBucket.S3 != nil
}

func ReplicaLock() time.Duration {
	return replicaLock
}

func isNotFound(err error) bool {
	var nf *types.NotFound
	var nsk *types.NoSuchKey
	if errors.As(err, &nf) || errors.As(err, &nsk) {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey")
}

func (b S3Bucket) Size(ctx context.Context, key string) (int64, error) {
	out, err := b.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.Name), Key: aws.String(key)})
	if isNotFound(err) {
		return 0, ErrNoObject
	}
	if err != nil {
		return 0, err
	}
	return aws.ToInt64(out.ContentLength), nil
}

func (b S3Bucket) Download(ctx context.Context, key string, w io.Writer) (int64, error) {
	out, err := b.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.Name), Key: aws.String(key)})
	if isNotFound(err) {
		return 0, ErrNoObject
	}
	if err != nil {
		return 0, err
	}
	defer out.Body.Close()
	return io.Copy(w, out.Body)
}

func (b S3Bucket) PutLocked(ctx context.Context, key string, f *os.File, size int64, until time.Time, class string) error {
	in := &s3.PutObjectInput{
		Body:          f,
		Bucket:        aws.String(b.Name),
		Key:           aws.String(key),
		ContentType:   aws.String("application/octet-stream"),
		ContentLength: aws.Int64(size),
		StorageClass:  types.StorageClass(class),
	}
	if !until.IsZero() {
		in.ObjectLockMode = types.ObjectLockModeCompliance
		in.ObjectLockRetainUntilDate = aws.Time(until)
		in.ChecksumAlgorithm = types.ChecksumAlgorithmCrc32
	}
	_, err := b.S3.PutObject(ctx, in)
	return err
}

func (b S3Bucket) ExtendLock(ctx context.Context, key string, until time.Time) error {
	_, err := b.S3.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{
		Bucket: aws.String(b.Name),
		Key:    aws.String(key),
		Retention: &types.ObjectLockRetention{
			Mode:            types.ObjectLockRetentionModeCompliance,
			RetainUntilDate: aws.Time(until),
		},
	})
	return err
}

func (b S3Bucket) EraseAll(ctx context.Context, key string) error {
	out, err := b.S3.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(b.Name),
		Prefix: aws.String(key),
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "NotImplemented" {
			return b.Delete(ctx, key)
		}
		return err
	}
	var versions []*string
	for _, v := range out.Versions {
		if aws.ToString(v.Key) == key {
			versions = append(versions, v.VersionId)
		}
	}
	for _, m := range out.DeleteMarkers {
		if aws.ToString(m.Key) == key {
			versions = append(versions, m.VersionId)
		}
	}
	if len(versions) == 0 {
		return b.Delete(ctx, key)
	}
	for _, v := range versions {
		if _, err := b.S3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    aws.String(b.Name),
			Key:       aws.String(key),
			VersionId: v,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b S3Bucket) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(b.S3, &s3.ListObjectsV2Input{Bucket: aws.String(b.Name), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	return keys, nil
}
