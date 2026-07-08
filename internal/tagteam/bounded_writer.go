package tagteam

import (
	"bytes"
	"errors"
	"fmt"
)

var errOutputLimitExceeded = errors.New("output exceeded configured byte limit")

type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	if limit <= 0 {
		limit = 2 * 1024 * 1024
	}
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return 0, errOutputLimitExceeded
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return int(remaining), errOutputLimitExceeded
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *boundedBuffer) String() string {
	return b.buf.String()
}

func (b *boundedBuffer) Exceeded() bool {
	return b.truncated
}

func outputLimitError(label string, limit int64) error {
	return fmt.Errorf("%s output exceeded max_output_bytes=%d", label, limit)
}
