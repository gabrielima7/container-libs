package archive

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/private"
)

var _ private.ImageSource = (*ociArchiveImageSource)(nil)

// Returns a path to an archive where index.json is an escaping symlink.
func archiveWithEscapingIndexJSON(t *testing.T) string {
	escapingDir := t.TempDir()
	escapingIndexPath := filepath.Join(escapingDir, "index.json")
	err := os.WriteFile(escapingIndexPath, []byte(
		`{
  "schemaVersion": 2,
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:eaa95f3cfaac07c8a5153eb77c933269586ad0226c83405776be08547e4d2a18",
      "size": 476,
      "annotations": {
        "org.opencontainers.image.ref.name": "latest"
      }
    }
  ]
}`), 0o600)
	require.NoError(t, err)

	archivePath := filepath.Join(t.TempDir(), "archive.tar")
	archiveWriter, err := os.Create(archivePath)
	require.NoError(t, err)
	tarWriter := tar.NewWriter(archiveWriter)
	err = tarWriter.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "index.json",
		Linkname: escapingIndexPath,
		Size:     int64(len(escapingIndexPath)),
		Mode:     0o755,
	})
	require.NoError(t, err)
	err = tarWriter.Close()
	require.NoError(t, err)
	err = archiveWriter.Close()
	require.NoError(t, err)

	return archivePath
}

func TestNewImageSource(t *testing.T) {
	// This should test much more.

	// The extracted archive is confined using an *os.Root. (The full scope of that confinement is tested within oci/layout.)
	archivePath := archiveWithEscapingIndexJSON(t)
	ref, err := NewReference(archivePath, "latest")
	require.NoError(t, err)
	_, err = ref.NewImageSource(context.Background(), nil)
	assert.ErrorContains(t, err, "path escapes from parent")
}

func TestLoadManifestDescriptor(t *testing.T) {
	// Archive not found
	emptyDir := t.TempDir()
	archivePath := filepath.Join(emptyDir, "foo.ociarchive")
	ref, err := ParseReference(archivePath)
	require.NoError(t, err)
	_, err = LoadManifestDescriptorWithContext(nil, ref)
	assert.NotNil(t, err)
	var aerr ArchiveFileNotFoundError
	assert.ErrorAs(t, err, &aerr)
	assert.Equal(t, aerr.path, archivePath)

	// The extracted archive is confined using an *os.Root. (The full scope of that confinement is tested within oci/layout.)
	archivePath = archiveWithEscapingIndexJSON(t)
	ref, err = NewReference(archivePath, "latest")
	require.NoError(t, err)
	_, err = LoadManifestDescriptorWithContext(nil, ref)
	assert.ErrorContains(t, err, "path escapes from parent")
}
