package config

// lockDir is a no-op on Windows for now (Windows support comes later):
// concurrent edits from two processes can lose one of them there.
func lockDir(string) (func(), error) { return func() {}, nil }
