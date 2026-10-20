package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	// s3PartSize is the multipart part size. S3 requires every part but the
	// last to be at least 5 MiB, so an object up to this size is uploaded
	// with one PutObject and anything larger in parts of this size.
	s3PartSize = 5 << 20
	// s3DeleteBatch is the most keys DeleteObjects accepts per request.
	s3DeleteBatch = 1000
)

// S3Store is a Storage on an S3-compatible service such as Cloudflare R2 or
// MinIO. Keys are object keys in one bucket, used as they are.
type S3Store struct {
	client   *s3.Client
	bucket   string
	endpoint string
}

var _ Storage = (*S3Store)(nil)

// S3Config describes the service to connect to.
type S3Config struct {
	Endpoint        string // base URL, e.g. https://<account>.r2.cloudflarestorage.com or http://127.0.0.1:9000
	Bucket          string
	Region          string // "auto" for Cloudflare R2; empty means "auto"
	AccessKeyID     string
	SecretAccessKey string
}

// NewS3 returns an S3Store for cfg. It makes no network calls. Requests use
// path-style addressing and checksums only where the service requires them,
// which is what R2 and MinIO accept.
func NewS3(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("storage: s3 needs an endpoint, bucket, access key ID and secret access key")
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	client := s3.NewFromConfig(aws.Config{
		Region:                     region,
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
	})
	return &S3Store{client: client, bucket: cfg.Bucket, endpoint: cfg.Endpoint}, nil
}

// Check reports whether the bucket exists and the credentials can reach it,
// with a HeadBucket request. The server calls it at startup so a wrong
// bucket name or credentials fail there, not as "not found" on every Preview.
func (s *S3Store) Check(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("storage: cannot access bucket %q at %s: %w", s.bucket, s.endpoint, err)
	}
	return nil
}

// Put uploads r as the object at key. Up to one part's worth of data goes
// in a single PutObject, with its length known up front. Anything larger is
// sent as multipart, and the upload is aborted if r or S3 fails, so no
// partial object appears.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidKey(key); err != nil {
		return err
	}
	src := ctxReader{ctx, r}
	buf := make([]byte, s3PartSize)
	n, err := io.ReadFull(src, buf)
	if err != nil {
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("storage: put %s: %w", key, err)
		}
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(s.bucket),
			Key:           aws.String(key),
			Body:          bytes.NewReader(buf[:n]),
			ContentLength: aws.Int64(int64(n)),
		})
		if err != nil {
			return fmt.Errorf("storage: put %s: %w", key, err)
		}
		return nil
	}
	return s.putMultipart(ctx, key, src, buf)
}

// putMultipart streams src, whose first part is already in buf, as a
// multipart upload. buf holds one full part when called.
func (s *S3Store) putMultipart(ctx context.Context, key string, src io.Reader, buf []byte) error {
	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	uploadID := created.UploadId
	fail := func(err error) error {
		// Abort even if ctx ended the upload, so no parts are left billing.
		s.client.AbortMultipartUpload(context.WithoutCancel(ctx), &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(s.bucket),
			Key:      aws.String(key),
			UploadId: uploadID,
		})
		return fmt.Errorf("storage: put %s: %w", key, err)
	}

	var parts []types.CompletedPart
	n, err := len(buf), error(nil) // buf is full: the caller's first read filled it
	for num := int32(1); ; num++ {
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fail(err)
		}
		if n > 0 {
			out, uerr := s.client.UploadPart(ctx, &s3.UploadPartInput{
				Bucket:        aws.String(s.bucket),
				Key:           aws.String(key),
				UploadId:      uploadID,
				PartNumber:    aws.Int32(num),
				Body:          bytes.NewReader(buf[:n]),
				ContentLength: aws.Int64(int64(n)),
			})
			if uerr != nil {
				return fail(uerr)
			}
			parts = append(parts, types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(num)})
		}
		if err != nil { // a short read is the final part
			break
		}
		n, err = io.ReadFull(src, buf)
	}

	if _, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		return fail(err)
	}
	return nil
}

// Open issues a HEAD for the size and modification time. The body is read
// lazily by s3Reader, so Open costs one request and a seek-then-read costs
// one ranged GET.
func (s *S3Store) Open(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	if err := ValidKey(key); err != nil {
		return Object{}, err
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return Object{}, fmt.Errorf("storage: open %s: %w (%w)", key, ErrNotFound, err)
		}
		return Object{}, fmt.Errorf("storage: open %s: %w", key, err)
	}
	size := aws.ToInt64(head.ContentLength)
	return Object{
		ReadSeekCloser: &s3Reader{ctx: ctx, store: s, key: key, size: size},
		Size:           size,
		ModTime:        aws.ToTime(head.LastModified),
	}, nil
}

// List yields every object under prefix in key order, one page at a time.
func (s *S3Store) List(ctx context.Context, prefix string) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(ObjectInfo{}, err)
			return
		}
		if err := ValidPrefix(prefix); err != nil {
			yield(ObjectInfo{}, err)
			return
		}
		pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket),
			Prefix: aws.String(prefix),
		})
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				yield(ObjectInfo{}, fmt.Errorf("storage: list %s: %w", prefix, err))
				return
			}
			for _, o := range page.Contents {
				info := ObjectInfo{
					Key:     aws.ToString(o.Key),
					Size:    aws.ToInt64(o.Size),
					ModTime: aws.ToTime(o.LastModified),
				}
				if !yield(info, nil) {
					return
				}
			}
		}
	}
}

// DeletePrefix deletes every object under prefix in batches of up to 1000
// keys. Per-key failures from S3 are returned together after the rest of
// the batches have been attempted.
func (s *S3Store) DeletePrefix(ctx context.Context, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidPrefix(prefix); err != nil {
		return err
	}
	var errs []error
	var batch []types.ObjectIdentifier
	flush := func() {
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(true)},
		})
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, e := range out.Errors {
				errs = append(errs, fmt.Errorf("%s: %s: %s", aws.ToString(e.Key), aws.ToString(e.Code), aws.ToString(e.Message)))
			}
		}
		batch = nil
	}
	for info, err := range s.List(ctx, prefix) {
		if err != nil {
			return fmt.Errorf("storage: delete %s: %w", prefix, err)
		}
		batch = append(batch, types.ObjectIdentifier{Key: aws.String(info.Key)})
		if len(batch) == s3DeleteBatch {
			flush()
		}
	}
	if len(batch) > 0 {
		flush()
	}
	if len(errs) > 0 {
		return fmt.Errorf("storage: delete %s: %w", prefix, errors.Join(errs...))
	}
	return nil
}

// isS3NotFound reports whether err means the object does not exist. HeadObject
// returns no body, so a 404 there comes back as types.NotFound whatever the
// cause, including a missing bucket.
func isS3NotFound(err error) bool {
	var noKey *types.NoSuchKey
	var notFound *types.NotFound
	return errors.As(err, &noKey) || errors.As(err, &notFound)
}
