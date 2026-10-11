//go:build !windows

package config

import (
	"fmt"
	"os"
)

func checkKeyPermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0044 != 0 {
		return fmt.Errorf("key file %s is group/world readable; chmod 600 %s", path, path)
	}
	return nil
}
