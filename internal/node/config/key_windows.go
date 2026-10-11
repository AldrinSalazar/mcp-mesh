package config

import "os"

// Windows file modes do not describe ACL readability.
func checkKeyPermissions(_ string, _ os.FileInfo) error { return nil }
