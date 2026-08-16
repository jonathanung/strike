package tool

import (
	"context"
	"errors"

	"github.com/jonathanung/strike-cli/harness/safefile"
)

// mapSafefileErr converts safefile classified errors into tool CodedError values.
func mapSafefileErr(err error) error {
	if err == nil {
		return nil
	}
	var se *safefile.Error
	if !errors.As(err, &se) || se == nil {
		return err
	}
	switch se.Code {
	case safefile.CodeTimeout:
		return ErrTimeout(se.Error())
	case safefile.CodeSpecialFile, safefile.CodeSymlink, safefile.CodeNotRegular, safefile.CodeInvalidPath:
		return ErrPrecondition(se.Error())
	default:
		return ErrPrecondition(se.Error())
	}
}

// safeReadFile reads a regular file with FIFO/special rejection and timeout.
func safeReadFile(ctx context.Context, path string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := safefile.ReadFile(ctx, path)
	if err != nil {
		return nil, mapSafefileErr(err)
	}
	return data, nil
}

// pathIdentity normalizes path for grant/ownership matching.
func pathIdentity(path string) (string, error) {
	id, err := safefile.Identity(path)
	if err != nil {
		return "", mapSafefileErr(err)
	}
	return id, nil
}
