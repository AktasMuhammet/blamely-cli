package gitutil

import (
	"io/fs"
	"os"
)

// pathStat is split out so we can swap it in tests if needed.
func pathStat(p string) (fs.FileInfo, error) {
	return os.Stat(p)
}
