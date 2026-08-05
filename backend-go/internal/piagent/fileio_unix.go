//go:build !windows

package piagent

import "os"

func replaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}
