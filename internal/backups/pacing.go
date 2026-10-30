package backups

import (
	"context"
	"io"
	"time"
)

// Pace only the transport stream, after signing; seekable upload bodies stay
// available to the SDK for signing and retries. Memory stays bounded by callers.
type pacedStorageBody struct {
	ctx         context.Context
	body        io.ReadCloser
	rate, total int64
	started     time.Time
}

func PaceTransfer(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	return newPacedStorageBody(ctx, body, 32<<20)
}

func newPacedStorageBody(ctx context.Context, body io.ReadCloser, rate int64) io.ReadCloser {
	return &pacedStorageBody{ctx: ctx, body: body, rate: rate, started: time.Now()}
}
func (p *pacedStorageBody) Read(buffer []byte) (int, error) {
	if e := p.ctx.Err(); e != nil {
		return 0, e
	}
	n, e := p.body.Read(buffer)
	p.total += int64(n)
	if n > 0 && p.rate > 0 && p.total > 64<<10 {
		due := time.Duration(float64(p.total-(64<<10)) / float64(p.rate) * float64(time.Second))
		delay := due - time.Since(p.started)
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-p.ctx.Done():
				return 0, p.ctx.Err()
			}
		}
	}
	return n, e
}
func (p *pacedStorageBody) Close() error { return p.body.Close() }
