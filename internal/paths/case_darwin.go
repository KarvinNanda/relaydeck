package paths

// fixCase restores the on-disk letter case, because the default macOS
// filesystem is case-insensitive and Claude Code uses the on-disk spelling.
func fixCase(p string) (string, error) { return walkCase(p) }
