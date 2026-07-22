package archive

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/fileutils"
	"go.podman.io/storage/pkg/ioutils"
)

func TestUnpackLayer(t *testing.T) {
	hdrEditor := func(hdr *tar.Header) {
		hdr.Uid = os.Getuid()
		hdr.Gid = os.Getgid()
	}

	// A smoke test; TestExtractTarFileEntry tests that the correct kinds of individual files are created, with the right properties.
	dest := t.TempDir()
	reader := tarStream(t, []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "dir", Mode: 0o700},
		{Typeflag: tar.TypeReg, Name: "regular", Mode: 0o600},
		// tar.TypeBlock, tar.typeChar untested because they require root privileges
		{Typeflag: tar.TypeFifo, Name: "fifo", Mode: 0o600},
		{Typeflag: tar.TypeLink, Name: "link", Linkname: "regular", Mode: 0o600},
		{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "dangling/local/target", Mode: 0o700},
	}, hdrEditor)
	_, err := UnpackLayer(dest, reader, nil)
	assert.NoError(t, err)

	// Paths are confined to the destination
	for i, headers := range [][]*tar.Header{
		{ // Direct overwrite
			{Typeflag: tar.TypeReg, Name: "../victim/hello", Mode: 0o600},
		},
		{ // Overwrite through an escaping symlink to directory
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "../victim", Mode: 0o755},
			{Typeflag: tar.TypeReg, Name: "symlink/hello", Mode: 0o600},
		},
		{ // Overwrite through an escaping symlink directly to victim
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "../victim/hello", Mode: 0o755},
			{Typeflag: tar.TypeReg, Name: "symlink", Mode: 0o600},
		},
		{ // Overwrite through an absolute symlink directly to victim
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "@TOP@/victim/hello", Mode: 0o644},
			{Typeflag: tar.TypeReg, Name: "symlink", Mode: 0o600},
		},
	} {
		t.Run(fmt.Sprintf("Breakout%d", i), func(t *testing.T) {
			err := testBreakout(t, breakoutUnpack, headers)
			assert.NoError(t, err)
		})
	}

	for _, preexisting := range []bool{true, false} {
		for _, destSuffix := range []string{"", "/"} {
			for _, rootName := range []string{".", "/"} {
				// TypeDir entries for dest are accepted.
				t.Run(fmt.Sprintf("dir root dest=%s, preexisting=%t, dir=%s", destSuffix, preexisting, rootName), func(t *testing.T) {
					dest := filepath.Join(t.TempDir(), "dest")
					if preexisting {
						err := os.Mkdir(dest, 0o700)
						require.NoError(t, err)
					}

					reader := tarStream(t, []*tar.Header{
						{Typeflag: tar.TypeDir, Name: rootName, Mode: 0o700},
						{Typeflag: tar.TypeReg, Name: filepath.Join(rootName, "file"), Mode: 0o600},
					}, hdrEditor)
					_, err := UnpackLayer(dest+destSuffix, reader, nil)
					require.NoError(t, err)

					fi, err := os.Lstat(dest)
					require.NoError(t, err)
					assert.True(t, fi.IsDir())
				})
			}
		}
	}

	// Parent directory is automatically created
	for _, c := range []struct {
		relDest, fileName string
		dirs              []string // relative to the temporary directory, not relDest (e.g. can be relDest)
	}{
		{relDest: ".", fileName: "file", dirs: []string{"."}},                                            // Pre-existing destination
		{relDest: ".", fileName: "dir/file", dirs: []string{".", "dir"}},                                 // Directory within the destination
		{relDest: "nonexistent", fileName: "file", dirs: []string{"nonexistent"}},                        // Even the destination may be created
		{relDest: "nonexistent", fileName: "dir/file", dirs: []string{"nonexistent", "nonexistent/dir"}}, // Destination + dir within both created
	} {
		t.Run(c.relDest+"|"+c.fileName, func(t *testing.T) {
			top := t.TempDir()
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeReg, Name: c.fileName, Mode: 0o600},
			}, hdrEditor)
			dest := filepath.Join(top, c.relDest) // No Mkdir(dest)!
			_, err := UnpackLayer(dest, reader, nil)
			assert.NoError(t, err)
			fi, err := os.Lstat(filepath.Join(dest, c.fileName))
			require.NoError(t, err)
			assert.True(t, fi.Mode().IsRegular())
			for _, dir := range c.dirs {
				fi, err := os.Lstat(filepath.Join(top, dir))
				require.NoError(t, err)
				assert.True(t, fi.IsDir())
			}
		})
	}

	t.Run("unused plnk", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh..wh.plnk/file", Mode: 0o600},
		}, hdrEditor)
		_, err := UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		assertDirIsPLNKOnly(t, dest)
	})
	t.Run("a non-regular-file plnk", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeFifo, Name: ".wh..wh.plnk/fifo", Mode: 0o700},
			{Typeflag: tar.TypeLink, Name: "fifo-link", Linkname: ".wh..wh.plnk/fifo", Mode: 0o600},
		}, hdrEditor)
		_, err := UnpackLayer(dest, reader, nil)
		assert.Error(t, err)
		assertDirIsPLNKOnly(t, dest)
	})
	t.Run("valid plnk", func(t *testing.T) {
		dest := t.TempDir()
		plinkPath := ".wh..wh.plnk/file"
		reader, writer := io.Pipe()
		go func() {
			tw := tar.NewWriter(writer)
			for _, contents := range []string{"contents1", "contents 2 with a different length"} {
				hdr := tar.Header{Typeflag: tar.TypeReg, Name: plinkPath, Size: int64(len(contents)), Mode: 0o700}
				hdrEditor(&hdr)
				err := tw.WriteHeader(&hdr)
				require.NoError(t, err)
				_, err = tw.Write([]byte(contents))
				require.NoError(t, err)
			}
			hdr := tar.Header{Typeflag: tar.TypeLink, Name: "file-link", Linkname: plinkPath, Mode: 0o600}
			hdrEditor(&hdr)
			err := tw.WriteHeader(&hdr)
			require.NoError(t, err)
			tw.Close()
			writer.Close()
		}()

		_, err := UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, dest)
		assert.Equal(t, []string{".wh..wh.plnk", "file-link"}, contents)
		contents = readdirNames(t, filepath.Join(dest, ".wh..wh.plnk"))
		assert.Empty(t, contents)
		fi, err := os.Lstat(filepath.Join(dest, "file-link"))
		require.NoError(t, err)
		assert.True(t, fi.Mode().IsRegular()) // The current implementation does not actually create hard links
		data, err := os.ReadFile(filepath.Join(dest, "file-link"))
		require.NoError(t, err)
		assert.Equal(t, []byte("contents 2 with a different length"), data)
	})

	// Other archive members with WhiteoutMetaPrefix are ignored
	t.Run("unrecognized metadata prefix", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh..wh.something_else", Mode: 0o600},
		}, hdrEditor)
		_, err := UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, dest)
		assert.Empty(t, contents)
	})

	// WhiteoutOpaqueDir
	for _, path := range []string{"dir", "."} {
		t.Run(path, func(t *testing.T) {
			victimDir := t.TempDir()
			victim := filepath.Join(victimDir, "victim")
			err := os.WriteFile(victim, []byte("content"), 0o600)
			require.NoError(t, err)
			fi1, err := os.Lstat(victim)
			require.NoError(t, err)

			dest := t.TempDir()
			err = os.Mkdir(filepath.Join(dest, "dir"), 0o700)
			require.NoError(t, err)
			for _, subdir := range []string{".", "dir"} {
				err := os.WriteFile(filepath.Join(dest, subdir, "oldfile"), []byte("content"), 0o600)
				require.NoError(t, err)
				err = os.Symlink(victim, filepath.Join(dest, subdir, "oldsymlink"))
				require.NoError(t, err)
			}
			whiteoutRelPath := path + "/.wh..wh..opq"
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeReg, Name: "newfile", Mode: 0o600},
				{Typeflag: tar.TypeSymlink, Name: "newsymlink", Linkname: victim, Mode: 0o700},
				{Typeflag: tar.TypeReg, Name: "dir/newfile", Mode: 0o600},
				{Typeflag: tar.TypeSymlink, Name: "dir/newsymlink", Linkname: victim, Mode: 0o700},
				{Typeflag: tar.TypeReg, Name: whiteoutRelPath, Mode: 0o600},
			}, hdrEditor)
			_, err = UnpackLayer(dest, reader, nil)
			require.NoError(t, err)
			// The opaque directory exists but was pre-existing files were removed.
			opaquePath := filepath.Join(dest, path)
			fi, err := os.Lstat(opaquePath)
			require.NoError(t, err)
			assert.True(t, fi.IsDir())
			contents := readdirNames(t, opaquePath)
			assert.Equal(t, []string{"newfile", "newsymlink"}, contents)
			// No other files were affected
			if opaquePath != dest {
				contents := readdirNames(t, dest)
				assert.Equal(t, []string{"dir", "newfile", "newsymlink", "oldfile", "oldsymlink"}, contents)
			}

			err = fileutils.Lexists(filepath.Join(dest, whiteoutRelPath))
			require.Error(t, err)
			assert.ErrorIs(t, err, os.ErrNotExist)
			// Symlink targets were not modified in any way
			fi2, err := os.Lstat(victim)
			require.NoError(t, err)
			assertCtimeMatches(t, fi1, fi2)
		})
	}
	// WhiteoutOpaqueDir without an explicit parent directory entry
	// happens to work, because we create the parent as if we were going to create
	// a regular file.
	t.Run("opaque missing", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "dir/.wh..wh..opq", Mode: 0o600},
		}, hdrEditor)
		_, err := UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		fi, err := os.Lstat(filepath.Join(dest, "dir"))
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
	})
	// WhiteoutOpaqueDir targeting a symlink
	t.Run("opaque symlink", func(t *testing.T) {
		victim := t.TempDir()
		err := os.WriteFile(filepath.Join(victim, "file"), []byte("content"), 0o600)
		require.NoError(t, err)
		dest := t.TempDir()
		symlinkPath := filepath.Join(dest, "symlink")
		err = os.Symlink(victim, symlinkPath)
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "symlink/.wh..wh..opq", Mode: 0o600},
		}, hdrEditor)
		_, err = UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, victim)
		assert.Equal(t, []string{"file"}, contents) // The target of an escaping symlink is unaffected
		// The symlink itself is not affected.
		fi, err := os.Lstat(symlinkPath)
		require.NoError(t, err)
		assert.True(t, fi.Mode()&os.ModeSymlink != 0)
	})

	// Ordinary whiteout
	t.Run("whiteout over nothing", func(t *testing.T) {
		dest := t.TempDir()
		err := os.WriteFile(filepath.Join(dest, "unaffected"), []byte("content"), 0o600)
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh.nonexistent", Mode: 0o600},
		}, hdrEditor)
		_, err = UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, dest)
		assert.Equal(t, []string{"unaffected"}, contents)
	})
	// Whiteout over an existing file
	t.Run("whiteout over regular file", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "unaffected", Mode: 0o600},
			{Typeflag: tar.TypeReg, Name: "file", Mode: 0o600},
			{Typeflag: tar.TypeReg, Name: ".wh.file", Mode: 0o600},
		}, hdrEditor)
		_, err := UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, dest)
		assert.Equal(t, []string{"unaffected"}, contents)
	})
	// Whiteout over an existing directory
	t.Run("whiteout over directory", func(t *testing.T) {
		dest := t.TempDir()
		// Create it separately, otherwise UnpackLayer tries to change its times at the very end, after it is already gone.
		err := os.Mkdir(filepath.Join(dest, "dir"), 0o700)
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "unaffected", Mode: 0o600},
			{Typeflag: tar.TypeReg, Name: ".wh.dir", Mode: 0o600},
		}, hdrEditor)
		_, err = UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, dest)
		assert.Equal(t, []string{"unaffected"}, contents)
	})
	// Whiteout over an existing symlink
	t.Run("whiteout over symlink", func(t *testing.T) {
		victim := t.TempDir()
		fi1, err := os.Lstat(victim)
		require.NoError(t, err)
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: victim, Mode: 0o700},
			{Typeflag: tar.TypeReg, Name: ".wh.symlink", Mode: 0o600},
		}, hdrEditor)
		_, err = UnpackLayer(dest, reader, nil)
		require.NoError(t, err)
		contents := readdirNames(t, dest)
		assert.Empty(t, contents)
		// victim was not affected
		fi2, err := os.Lstat(victim)
		require.NoError(t, err)
		assertCtimeMatches(t, fi1, fi2)
	})
	// Whiteout using a metadata prefix, in a subdirectory, is not treated specially.
	for _, whiteoutName := range []string{".wh..wh.plnk", ".wh..wh.something_else"} {
		t.Run("whiteout "+whiteoutName+" in subdirectory", func(t *testing.T) {
			dest := t.TempDir()
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeReg, Name: "dir/unaffected", Mode: 0o600},
				{Typeflag: tar.TypeReg, Name: "dir/" + strings.TrimPrefix(whiteoutName, ".wh."), Mode: 0o600},
				{Typeflag: tar.TypeReg, Name: "dir/" + whiteoutName, Mode: 0o600},
			}, hdrEditor)
			_, err := UnpackLayer(dest, reader, nil)
			require.NoError(t, err)
			contents := readdirNames(t, filepath.Join(dest, "dir"))
			assert.Equal(t, []string{"unaffected"}, contents)
		})
	}

	// Overwriting pre-existing files
	for _, c := range []struct {
		tarTypes     []byte
		expectedType fs.FileMode
	}{
		{tarTypes: []byte{tar.TypeReg, tar.TypeReg}, expectedType: fs.FileMode(0)}, // reg -> reg
		{tarTypes: []byte{tar.TypeDir, tar.TypeDir}, expectedType: fs.ModeDir},     // dir -> dir
		{tarTypes: []byte{tar.TypeReg, tar.TypeDir}, expectedType: fs.ModeDir},     // reg -> dir
		{tarTypes: []byte{tar.TypeDir, tar.TypeReg}, expectedType: fs.FileMode(0)}, // dir -> reg
	} {
		t.Run("", func(t *testing.T) {
			dest := t.TempDir()
			hdrs := []*tar.Header{}
			for _, tarType := range c.tarTypes {
				hdrs = append(hdrs, &tar.Header{Typeflag: tarType, Name: "test", Mode: 0o700})
			}
			reader := tarStream(t, hdrs, hdrEditor)
			_, err := UnpackLayer(dest, reader, nil)
			require.NoError(t, err)
			fi, err := os.Lstat(filepath.Join(dest, "test"))
			require.NoError(t, err)
			assert.Equal(t, c.expectedType, fi.Mode().Type())
		})
	}

	// Resolving plnk hard links has been mostly tested above.
	t.Run("dangling plnk", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeLink, Name: "link", Linkname: ".wh..wh.plnk/never-created", Mode: 0o600},
		}, hdrEditor)
		_, err := UnpackLayer(dest, reader, nil)
		assert.Error(t, err)
		contents := readdirNames(t, dest)
		assert.Empty(t, contents)
	})

	// Directory times are set correctly even if we create files inside them.
	mtime := time.Unix(1, 0)
	atime := time.Unix(2, 0)
	t.Run("directory times", func(t *testing.T) {
		dest = t.TempDir()
		// An explicit FormatPAX is necessary, otherwise archive/tar prefers to use a simpler header which does not encode AccessTime,
		reader = tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeDir, Name: "dir", Mode: 0o700, ModTime: mtime, AccessTime: atime, Format: tar.FormatPAX},
			{Typeflag: tar.TypeReg, Name: "dir/inside", Mode: 0o600, ModTime: mtime, AccessTime: atime, Format: tar.FormatPAX},
		}, hdrEditor)
		_, err = UnpackLayer(dest, reader, nil)
		assert.NoError(t, err)
		for _, path := range []string{"dir", "dir/inside"} {
			t.Run(path, func(t *testing.T) {
				fi, err := os.Lstat(filepath.Join(dest, path))
				require.NoError(t, err)
				assert.Equal(t, mtime, fi.ModTime())
				assertAtime(t, atime, fi)
			})
		}
	})

	// Setting BSD flags of directories is untested
}

func assertDirIsPLNKOnly(t *testing.T, dest string) {
	// This accepts / “tests for” a pre-existing bug: we create the parent directory before treating plnk specially.
	// This is NOT a commitment that the behavior will stay that way.
	contents := readdirNames(t, dest)
	assert.Equal(t, []string{".wh..wh.plnk"}, contents)
	contents = readdirNames(t, filepath.Join(dest, ".wh..wh.plnk"))
	assert.Empty(t, contents)
}

func readdirNames(t *testing.T, dir string) []string {
	f, err := os.Open(dir)
	require.NoError(t, err)
	defer f.Close()
	names, err := f.Readdirnames(0)
	require.NoError(t, err)
	slices.Sort(names)
	return names
}

func TestApplyLayerInvalidFilenames(t *testing.T) {
	// TODO Windows: Figure out how to fix this test.
	if runtime.GOOS == windows {
		t.Skip("Passes but hits breakoutError: platform and architecture is not supported")
	}
	for i, headers := range [][]*tar.Header{
		{
			{
				Name:     "../victim/dotdot",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{
			{
				// Note the leading slash
				Name:     "/../victim/slash-dotdot",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutApplyLayer, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestApplyLayerInvalidHardlink(t *testing.T) {
	if runtime.GOOS == windows {
		t.Skip("TypeLink support on Windows")
	}
	for i, headers := range [][]*tar.Header{
		{ // try reading victim/hello (../)
			{
				Name:     "dotdot",
				Typeflag: tar.TypeLink,
				Linkname: "../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (/../)
			{
				Name:     "slash-dotdot",
				Typeflag: tar.TypeLink,
				// Note the leading slash
				Linkname: "/../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try writing victim/file
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim/file",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (hardlink, symlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "symlink",
				Typeflag: tar.TypeSymlink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // Try reading victim/hello (hardlink, hardlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "hardlink",
				Typeflag: tar.TypeLink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // Try removing victim directory (hardlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutApplyLayer, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestApplyLayerInvalidSymlink(t *testing.T) {
	if runtime.GOOS == windows {
		t.Skip("TypeSymLink support on Windows")
	}
	for i, headers := range [][]*tar.Header{
		{ // try reading victim/hello (../)
			{
				Name:     "dotdot",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (/../)
			{
				Name:     "slash-dotdot",
				Typeflag: tar.TypeSymlink,
				// Note the leading slash
				Linkname: "/../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try writing victim/file
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim/file",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (symlink, symlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "symlink",
				Typeflag: tar.TypeSymlink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (symlink, hardlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "hardlink",
				Typeflag: tar.TypeLink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // try removing victim directory (symlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutApplyLayer, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestApplyLayerWhiteouts(t *testing.T) {
	// TODO Windows: Figure out why this test fails
	if runtime.GOOS == windows {
		t.Skip("Failing on Windows")
	}

	wd := t.TempDir()

	base := []string{
		".baz",
		"bar/",
		"bar/bax",
		"bar/bay/",
		"baz",
		"foo/",
		"foo/.abc",
		"foo/.bcd/",
		"foo/.bcd/a",
		"foo/cde/",
		"foo/cde/def",
		"foo/cde/efg",
		"foo/fgh",
		"foobar",
	}

	type tcase struct {
		change, expected []string
	}

	tcases := []tcase{
		{
			base,
			base,
		},
		{
			[]string{
				".bay",
				".wh.baz",
				"foo/",
				"foo/.bce",
				"foo/.wh..wh..opq",
				"foo/cde/",
				"foo/cde/efg",
			},
			[]string{
				".bay",
				".baz",
				"bar/",
				"bar/bax",
				"bar/bay/",
				"foo/",
				"foo/.bce",
				"foo/cde/",
				"foo/cde/efg",
				"foobar",
			},
		},
		{
			[]string{
				".bay",
				".wh..baz",
				".wh.foobar",
				"foo/",
				"foo/.abc",
				"foo/.wh.cde",
				"bar/",
			},
			[]string{
				".bay",
				"bar/",
				"bar/bax",
				"bar/bay/",
				"foo/",
				"foo/.abc",
				"foo/.bce",
			},
		},
		{
			[]string{
				".abc",
				".wh..wh..opq",
				"foobar",
			},
			[]string{
				".abc",
				"foobar",
			},
		},
	}

	for i, tc := range tcases {
		l, err := makeTestLayer(t, tc.change)
		if err != nil {
			t.Fatal(err)
		}

		_, err = UnpackLayer(wd, l, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = l.Close()
		if err != nil {
			t.Fatal(err)
		}

		paths, err := readDirContents(wd)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(tc.expected, paths) {
			t.Fatalf("invalid files for layer %d: expected %q, got %q", i, tc.expected, paths)
		}
	}
}

func makeTestLayer(t *testing.T, paths []string) (io.ReadCloser, error) {
	tmpDir := t.TempDir()
	for _, p := range paths {
		if p[len(p)-1] == filepath.Separator {
			if err := os.MkdirAll(filepath.Join(tmpDir, p), 0o700); err != nil {
				return nil, err
			}
		} else {
			if err := os.WriteFile(filepath.Join(tmpDir, p), nil, 0o600); err != nil {
				return nil, err
			}
		}
	}
	archive, err := Tar(tmpDir, Uncompressed)
	if err != nil {
		return nil, err
	}
	return ioutils.NewReadCloserWrapper(archive, func() error {
		err := archive.Close()
		return err
	}), nil
}

func readDirContents(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel = rel + "/"
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
