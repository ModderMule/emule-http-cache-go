//go:build !unix

package storage

// freeSpace has no portable implementation off unix.
//
// Reporting "unknown" makes HasRoomFor fail open, which is the same thing the
// PHP server does when disk_free_space() cannot answer: a host that cannot
// measure its own volume must keep accepting uploads rather than refuse every
// single one.
func freeSpace(string) (int64, bool) {
	return 0, false
}
