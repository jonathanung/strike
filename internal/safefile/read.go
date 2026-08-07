package safefile

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

// DefaultReadTimeout bounds OpenRead/ReadFile when the context has no deadline.
// Prevents FIFO/special blocking opens from hanging a tool forever.
const DefaultReadTimeout = 5 * time.Second

// OpenRead opens path for reading after rejecting special files.
// Symlink leaves are followed (read policy) but the ultimate target must be a
// regular file. The open is bound by ctx or DefaultReadTimeout.
func OpenRead(ctx context.Context, path string) (*os.File, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := CheckLeaf(path, false); err != nil {
		return nil, err
	}
	// Re-check after potential race: ensure still not special via Lstat on final.
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if IsSpecialMode(fi.Mode()) {
		return nil, errf(CodeSpecialFile, path, "refusing special file %s %q", modeType(fi.Mode()), path)
	}
	if !fi.Mode().IsRegular() {
		return nil, errf(CodeNotRegular, path, "path is not a regular file: %q", path)
	}

	type result struct {
		f   *os.File
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := os.Open(path)
		ch <- result{f, err}
	}()

	timer := time.NewTimer(openTimeout(ctx))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.f != nil {
				_ = r.f.Close()
			}
		}()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errf(CodeTimeout, path, "timed out opening %q", path)
		}
		return nil, ctx.Err()
	case <-timer.C:
		go func() {
			if r := <-ch; r.f != nil {
				_ = r.f.Close()
			}
		}()
		return nil, errf(CodeTimeout, path, "timed out opening %q", path)
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		// TOCTOU: verify still regular after open.
		st, err := r.f.Stat()
		if err != nil {
			_ = r.f.Close()
			return nil, err
		}
		if IsSpecialMode(st.Mode()) || !st.Mode().IsRegular() {
			_ = r.f.Close()
			return nil, errf(CodeSpecialFile, path, "refusing non-regular file after open: %q", path)
		}
		return r.f, nil
	}
}

func openTimeout(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		d := time.Until(dl)
		if d > 0 {
			return d
		}
		return time.Millisecond // already expired; select will hit ctx.Done
	}
	return DefaultReadTimeout
}

// ReadFile reads the entire regular file at path with OpenRead bounds.
func ReadFile(ctx context.Context, path string) ([]byte, error) {
	f, err := OpenRead(ctx, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
