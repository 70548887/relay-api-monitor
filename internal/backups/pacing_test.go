package backups

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTransferPacingCancelsInsteadOfHoldingRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	body := newPacedStorageBody(ctx, io.NopCloser(strings.NewReader(strings.Repeat("x", 128<<10))), 1)
	defer body.Close()
	_, e := io.CopyBuffer(io.Discard, body, make([]byte, 128<<10))
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
