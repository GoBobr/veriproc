package console

import "os"

func mkdirAll(p string, mode os.FileMode) error { return os.MkdirAll(p, mode) }
func writeFile(p string, body []byte) error      { return os.WriteFile(p, body, 0o644) }
