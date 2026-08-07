package tool

import (
	"os"

	"github.com/jonathanung/strike-cli/internal/safefile"
)

// atomicWriteFile writes data to path via temp file + rename so readers never
// observe a partial file on local POSIX filesystems (same-directory rename is
// atomic). Preserves existing file mode when path already exists; otherwise
// uses perm (default 0o644). Refuses symlink leaves and special files via
// safefile.WriteFile (pair with resolveInWorkspace defenses).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return mapSafefileErr(safefile.WriteFile(path, data, perm))
}
