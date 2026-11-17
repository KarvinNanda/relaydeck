package paths

import (
	"path/filepath"
	"strings"
)

// fixCase upper-cases the drive letter and restores the on-disk letter case
// of every other component.
func fixCase(p string) (string, error) {
	vol := filepath.VolumeName(p)
	if len(vol) >= 2 && vol[1] == ':' {
		p = strings.ToUpper(vol[:1]) + p[1:]
	}
	return walkCase(p)
}
