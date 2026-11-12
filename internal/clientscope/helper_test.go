package clientscope

import "os"

func writeFile(p, b string) error { return os.WriteFile(p, []byte(b), 0o600) }
