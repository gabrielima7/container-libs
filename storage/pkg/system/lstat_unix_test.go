//go:build unix

package system

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLstat(t *testing.T) {
	_, file, invalid, symlink := prepareFiles(t)

	st, err := Lstat(file)
	require.NoError(t, err)
	assert.False(t, st.IsSymlink())

	st, err = Lstat(invalid)
	assert.Error(t, err, "did not return error for non-existing file")
	assert.Nil(t, st, "returned non-nil stat for non-existing file")

	st, err = Lstat(symlink)
	require.NoError(t, err)
	assert.True(t, st.IsSymlink())
}

func TestRootLstat(t *testing.T) {
	dir, file, invalid, symlink := prepareFiles(t)

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()

	file, err = filepath.Rel(dir, file)
	require.NoError(t, err)
	st, err := RootLstat(root, filepath.ToSlash(file))
	require.NoError(t, err)
	assert.False(t, st.IsSymlink())

	invalid, err = filepath.Rel(dir, invalid)
	require.NoError(t, err)
	st, err = RootLstat(root, filepath.ToSlash(invalid))
	assert.Error(t, err)
	assert.Nil(t, st)

	symlink, err = filepath.Rel(dir, symlink)
	require.NoError(t, err)
	st, err = RootLstat(root, filepath.ToSlash(symlink))
	require.NoError(t, err)
	assert.True(t, st.IsSymlink())

	st, err = RootLstat(root, "..")
	assert.Error(t, err)
	assert.Nil(t, st)
}
