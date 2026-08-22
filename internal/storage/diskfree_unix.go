//go:build unix

package storage

import "golang.org/x/sys/unix"

// freeSpace reports the bytes available to an unprivileged writer on the
// volume holding path.
func freeSpace(path string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}

	return int64(st.Bavail) * int64(st.Bsize), true
}
