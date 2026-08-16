package tool

import "context"

// StaticContract builds a v1 contract (shared by built-in and product tools).
func StaticContract(se SideEffect, id Idempotency) Contract {
	return staticContract(se, id)
}

// ResolveAllowedPath resolves path under the session workspace root, or —
// when path is absolute — under the optional session temporary directory.
func ResolveAllowedPath(workDir, tempDir, path string) (resolved, display string, err error) {
	return resolveAllowedPath(workDir, tempDir, path)
}

// SafeReadFile reads a regular file with FIFO/special rejection and timeout.
func SafeReadFile(ctx context.Context, path string) ([]byte, error) {
	return safeReadFile(ctx, path)
}

// CheckContentGuard scans proposed file content before it reaches disk.
func CheckContentGuard(ctx context.Context, tc *Context, rel, content string) error {
	return checkContentGuard(ctx, tc, rel, content)
}

// AllowedWriteFile re-validates path under workDir or the optional session
// temp root immediately before writing (atomic temp+rename).
func AllowedWriteFile(workDir, tempDir, path string, data []byte) error {
	return allowedWriteFile(workDir, tempDir, path, data)
}
