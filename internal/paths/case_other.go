//go:build !darwin && !windows

package paths

// fixCase does nothing here: other filesystems are case-sensitive, so the
// path as resolved is already the on-disk spelling.
func fixCase(p string) (string, error) { return p, nil }
