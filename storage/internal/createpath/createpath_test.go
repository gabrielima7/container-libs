package createpath

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCleanAbsPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"", "/"},
		{".", "/"},
		{"..", "/"},
		{"foo/../..", "/"},
		{"/foo/../..", "/"},
		{"./", "/"},
		{"../", "/"},
		{"/../", "/"},
		{"/./", "/"},
		{"foo", "/foo"},
		{"foo/bar", "/foo/bar"},
		{"/foo/bar/../baz", "/foo/baz"},
		{"/foo/./bar", "/foo/bar"},
		{"/foo/bar/../../baz", "/baz"},
		{"/././foo", "/foo"},
		{"../foo", "/foo"},
		{"./foo/bar/../..", "/"},
		{"foo/..", "/"},
		{"foo/../bar", "/bar"},
		{"//foo//bar", "/foo/bar"},
		{"foo/bar//baz/..", "/foo/bar"},
		{"../..", "/"},
		{".././..", "/"},
		{"../../.", "/"},
		{"/../../foo", "/foo"},
		{"../foo/bar/../baz", "/foo/baz"},
		{"../.././/.//../foo/./../bar/..", "/"},
		{"a/../.././/.//../foo/./../bar/..", "/"},
	}

	for _, test := range tests {
		assert.Equal(t, test.expected, CleanAbsPath(test.path), fmt.Sprintf("path %q failed", test.path))
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path         string
		expectedDir  string
		expectedBase string
	}{
		{"", "/", "."},
		{".", "/", "."},
		{"..", "/", "."},
		{"../..", "/", "."},
		{"../../..", "/", "."},
		{"../../../foo", "/", "foo"},
		{"../../../foo/..", "/", "."},
		{"../../../foo/./../foo/bar/baz", "/foo/bar", "baz"},
		{"../../../foo/bar", "/foo", "bar"},
		{"/", "/", "."},
		{"/.", "/", "."},
		{"/..", "/", "."},
		{"////foo////bar////", "/foo", "bar"},
		{"/foo", "/", "foo"},
		{"/foo/", "/", "foo"},
		{"/foo/bar", "/foo", "bar"},
		{"/foo/bar/", "/foo", "bar"},
		{"/foo/////bar/", "/foo", "bar"},
		{"/home/foo/file.txt", "/home/foo", "file.txt"},
		{"/home/foo////file.txt", "/home/foo", "file.txt"},
		{"file", "/", "file"},
		{"foo/", "/", "foo"},
		{"foo/.", "/", "foo"},
		{"foo/..", "/", "."},
		{"foo/../../bar", "/", "bar"},
		{"foo/bar/", "/foo", "bar"},
		{"foo/bar/..", "/", "foo"},
		{"foo/bar/../baz", "/foo", "baz"},
		{"foo/bar/baz/", "/foo/bar", "baz"},
		{"foo/file.txt", "/foo", "file.txt"},
	}

	for _, test := range tests {
		dir, base, err := SplitPath(test.path)
		assert.NoError(t, err)
		assert.Equal(t, test.expectedDir, dir, fmt.Sprintf("path %q: expected dir %q, got %q", test.path, test.expectedDir, dir))
		assert.Equal(t, test.expectedBase, base, fmt.Sprintf("path %q: expected base %q, got %q", test.path, test.expectedBase, base))
	}
}
