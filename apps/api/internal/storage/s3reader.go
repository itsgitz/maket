package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3Reader is the seekable body of an S3 object. It holds at most one open
// GET, starting at the current position. Read opens that GET when needed,
// and Seek to a different position drops it, so the next Read asks for the
// new range. Reads are sequential, so the open body always sits at pos.
type s3Reader struct {
	ctx    context.Context
	store  *S3Store
	key    string
	size   int64
	pos    int64
	body   io.ReadCloser // open GET positioned at pos; nil when none
	closed bool
}

var _ io.ReadSeekCloser = (*s3Reader)(nil)

func (r *s3Reader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.body == nil {
		out, err := r.store.client.GetObject(r.ctx, &s3.GetObjectInput{
			Bucket: aws.String(r.store.bucket),
			Key:    aws.String(r.key),
			Range:  aws.String(fmt.Sprintf("bytes=%d-", r.pos)),
		})
		if err != nil {
			if isS3NotFound(err) {
				return 0, fmt.Errorf("storage: read %s: %w (%w)", r.key, ErrNotFound, err)
			}
			return 0, fmt.Errorf("storage: read %s: %w", r.key, err)
		}
		r.body = out.Body
	}
	n, err := r.body.Read(p)
	r.pos += int64(n)
	if errors.Is(err, io.EOF) && r.pos < r.size {
		// The object shrank or the body was cut short: never report a
		// clean EOF before the advertised size.
		err = io.ErrUnexpectedEOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		r.closeBody()
		return n, fmt.Errorf("storage: read %s: %w", r.key, err)
	}
	return n, err
}

// Seek sets the position for the next Read. It never touches the network,
// and it drops the open body if the position changes.
func (r *s3Reader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("storage: seek %s: invalid whence %d", r.key, whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("storage: seek %s: negative position %d", r.key, abs)
	}
	if abs != r.pos {
		r.closeBody()
		r.pos = abs
	}
	return abs, nil
}

// Close releases the open body. Later calls to Read or Seek fail.
func (r *s3Reader) Close() error {
	r.closeBody()
	r.closed = true
	return nil
}

func (r *s3Reader) closeBody() {
	if r.body != nil {
		r.body.Close()
		r.body = nil
	}
}
