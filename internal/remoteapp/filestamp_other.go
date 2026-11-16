//go:build !unix

package remoteapp

import "os"

// inode is 0 where the platform has none; time and size still apply.
func inode(os.FileInfo) uint64 { return 0 }
