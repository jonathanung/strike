package local

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jonathanung/strike/internal/frontend/host"
)

// RawDocument is one workspace file's bytes for the raw view route
// (#1393): images rendered in the document viewer and downloads of other
// binary files.
type RawDocument struct {
	Path          string
	Size          int64
	MtimeUnixNano int64
	Data          []byte
}

// ErrRawTooLarge reports a file over the raw read cap.
var ErrRawTooLarge = errors.New("file is too large to view")

// ReadDocumentRaw reads path's bytes under exactly the confinement
// ReadDocument applies: the path must stay under the work root (no
// symlink anywhere in it, regular files only) and inside the allowed-roots
// ceiling when one is set. A file larger than max returns ErrRawTooLarge
// with Size filled in. ok is false when f is not the local files service,
// so callers can report the capability as unavailable.
func ReadDocumentRaw(f host.Files, path string, max int64) (doc RawDocument, ok bool, err error) {
	fs, isLocal := f.(*filesService)
	if !isLocal || fs == nil {
		return RawDocument{}, false, nil
	}
	work, ceilingOn, ceiling := fs.docRoots()
	resolved, rel, info, err := fs.statDocument(work, path)
	if err != nil {
		return RawDocument{}, true, err
	}
	if err := withinCeiling(resolved, ceilingOn, ceiling); err != nil {
		return RawDocument{}, true, err
	}
	tooLarge := func(size int64) (RawDocument, bool, error) {
		return RawDocument{Path: rel, Size: size}, true,
			fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrRawTooLarge, rel, size, max)
	}
	if info.Size() > max {
		return tooLarge(info.Size())
	}
	file, err := os.Open(resolved)
	if err != nil {
		return RawDocument{}, true, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return RawDocument{}, true, err
	}
	// The file may have grown between the stat and the read.
	if int64(len(data)) > max {
		return tooLarge(int64(len(data)))
	}
	return RawDocument{
		Path:          rel,
		Size:          int64(len(data)),
		MtimeUnixNano: info.ModTime().UnixNano(),
		Data:          data,
	}, true, nil
}
