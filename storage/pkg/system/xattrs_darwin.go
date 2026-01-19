package system

import (
	"bytes"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	// Value is larger than the maximum size allowed
	E2BIG unix.Errno = unix.E2BIG

	// Operation not supported
	ENOTSUP unix.Errno = unix.ENOTSUP

	// Not in x/sys/unix as of v0.40.0.
	O_RESOLVE_BENEATH = 0x00001000
)

// getxattr is the logic underlying Lgetxattr and Fgetxattr.
// Returns a []byte slice if the xattr is set and nil otherwise.
func getxattr(syscallName string, pathInError string, getSyscall func(dest []byte) (int, error)) ([]byte, error) {
	// Start with a 128 length byte array
	dest := make([]byte, 128)
	sz, errno := getSyscall(dest)

	for errno == unix.ERANGE {
		// Buffer too small, use zero-sized buffer to get the actual size
		sz, errno = getSyscall([]byte{})
		if errno != nil {
			return nil, &os.PathError{Op: syscallName, Path: pathInError, Err: errno}
		}
		dest = make([]byte, sz)
		sz, errno = getSyscall(dest)
	}

	switch {
	case errno == unix.ENOATTR:
		return nil, nil
	case errno != nil:
		return nil, &os.PathError{Op: syscallName, Path: pathInError, Err: errno}
	}

	return dest[:sz], nil
}

// Lgetxattr retrieves the value of the extended attribute identified by attr
// and associated with the given path in the file system.
// Returns a []byte slice if the xattr is set and nil otherwise.
func Lgetxattr(path string, attr string) ([]byte, error) {
	return getxattr("lgetxattr", path, func(dest []byte) (int, error) {
		return unix.Lgetxattr(path, attr, dest)
	})
}

// RootLgetxattr retrieves the value of the extended attribute identified by attr
// in fsPath (per fs.ValidPath) under root.
// Returns a []byte slice if the xattr is set and nil otherwise.
func RootLgetxattr(root *os.Root, fsPath string, attr string) ([]byte, error) {
	// We can’t use root.Open(fsPath) because it follows trailing symlinks.
	rootFD, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootFD.Close()
	fd, err := syscallConnControl(rootFD, func(rootFD uintptr) (int, error) {
		// macOS does not have O_PATH, so hope the user has enough permissions.
		return unix.Openat(int(rootFD), filepath.FromSlash(fsPath), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_SYMLINK|O_RESOLVE_BENEATH, 0)
	})
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)

	return getxattr("RootLgetxattr", fsPath, func(dest []byte) (int, error) {
		return unix.Fgetxattr(fd, attr, dest)
	})
}

// Lsetxattr sets the value of the extended attribute identified by attr
// and associated with the given path in the file system.
func Lsetxattr(path string, attr string, data []byte, flags int) error {
	if err := unix.Lsetxattr(path, attr, data, flags); err != nil {
		return &os.PathError{Op: "lsetxattr", Path: path, Err: err}
	}

	return nil
}

// Llistxattr lists extended attributes associated with the given path
// in the file system.
func Llistxattr(path string) ([]string, error) {
	dest := make([]byte, 128)
	sz, errno := unix.Llistxattr(path, dest)

	for errno == unix.ERANGE {
		// Buffer too small, use zero-sized buffer to get the actual size
		sz, errno = unix.Llistxattr(path, []byte{})
		if errno != nil {
			return nil, &os.PathError{Op: "llistxattr", Path: path, Err: errno}
		}

		dest = make([]byte, sz)
		sz, errno = unix.Llistxattr(path, dest)
	}
	if errno != nil {
		return nil, &os.PathError{Op: "llistxattr", Path: path, Err: errno}
	}

	var attrs []string
	for token := range bytes.SplitSeq(dest[:sz], []byte{0}) {
		if len(token) > 0 {
			attrs = append(attrs, string(token))
		}
	}

	return attrs, nil
}
