//go:build windows

package piagent

import "golang.org/x/sys/windows"

func replaceFile(tempPath, targetPath string) error {
	return windows.MoveFileEx(
		windows.StringToUTF16Ptr(tempPath),
		windows.StringToUTF16Ptr(targetPath),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
