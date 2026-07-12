package tagteam

import (
	"context"
	"testing"
)

func TestMaxOutputBytesInheritsRunLimitWhenRequestOmitsIt(t *testing.T) {
	ctx := context.WithValue(context.Background(), maxOutputBytesContextKey{}, int64(8*1024*1024))
	if got := maxOutputBytes(Request{Context: ctx}); got != 8*1024*1024 {
		t.Fatalf("inherited max output bytes = %d, want %d", got, 8*1024*1024)
	}
	if got := maxOutputBytes(Request{Context: ctx, MaxOutputBytes: 4096}); got != 4096 {
		t.Fatalf("explicit max output bytes = %d, want %d", got, 4096)
	}
}
