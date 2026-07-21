package upload_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

func TestBuildManifestOrderingAndIDs(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.onnx")
	in0 := filepath.Join(dir, "audio.bin")
	in1 := filepath.Join(dir, "mask.bin")
	ext := filepath.Join(dir, "weights.data")
	for _, p := range []string{model, in0, in1, ext} {
		require.NoError(t, os.WriteFile(p, []byte("content of "+filepath.Base(p)), 0o600))
	}

	specs, err := upload.BuildManifest(model, []string{in0, in1}, []string{ext}, nil)
	require.NoError(t, err)
	require.Len(t, specs, 4)

	// Order: model, inputs in flag order, external data. IDs f0..fN.
	assert.Equal(t, "f0", specs[0].ClientFileID)
	assert.Equal(t, upload.RoleModel, specs[0].Role)
	assert.Equal(t, "model.onnx", specs[0].Filename)
	assert.Equal(t, -1, specs[0].InputIndex)

	assert.Equal(t, "f1", specs[1].ClientFileID)
	assert.Equal(t, upload.RoleInput, specs[1].Role)
	assert.Equal(t, 0, specs[1].InputIndex, "input_index follows flag order")
	assert.Equal(t, "audio.bin", specs[1].Filename)

	assert.Equal(t, "f2", specs[2].ClientFileID)
	assert.Equal(t, 1, specs[2].InputIndex)

	assert.Equal(t, "f3", specs[3].ClientFileID)
	assert.Equal(t, upload.RoleExternalData, specs[3].Role)
	assert.Equal(t, -1, specs[3].InputIndex)

	for _, s := range specs {
		assert.NotEmpty(t, s.CRC32C, "%s digested", s.Filename)
		assert.Len(t, s.SHA256, 64)
		assert.Positive(t, s.Size)
	}
}

func TestBuildManifestDuplicateBasenames(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.onnx")
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o700))
	dup := filepath.Join(sub, "model.onnx")
	for _, p := range []string{model, dup} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}

	_, err := upload.BuildManifest(model, nil, []string{dup}, nil)
	require.ErrorIs(t, err, upload.ErrDuplicateFilename)
	assert.Contains(t, err.Error(), "model.onnx")
}

func TestBuildManifestEmptyFileRejected(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.onnx")
	require.NoError(t, os.WriteFile(model, nil, 0o600))
	_, err := upload.BuildManifest(model, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestCanonicalPathPreview(t *testing.T) {
	tests := []struct {
		role  string
		index int
		name  string
		want  string
	}{
		{upload.RoleModel, -1, "model.onnx", "{tag}/model.onnx"},
		{upload.RoleExternalData, -1, "weights.data", "{tag}/data/weights.data"},
		{upload.RoleInput, 0, "audio.bin", "{tag}/inputs/00_audio.bin"},
		{upload.RoleInput, 7, "b.bin", "{tag}/inputs/07_b.bin"},
		{upload.RoleInput, 12, "c.bin", "{tag}/inputs/12_c.bin"},
	}
	for _, tc := range tests {
		got := upload.CanonicalPathPreview(upload.FileSpec{Role: tc.role, InputIndex: tc.index, Filename: tc.name})
		assert.Equal(t, tc.want, got)
	}
}

func TestLoadManifestDoc(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.onnx")
	in := filepath.Join(dir, "i.bin")
	for _, p := range []string{model, in} {
		require.NoError(t, os.WriteFile(p, []byte("data"), 0o600))
	}
	doc := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(doc, []byte(`{
		"manifest_version": 2,
		"files": [
			{"path": `+jsonStr(model)+`, "role": "model"},
			{"path": `+jsonStr(in)+`, "role": "input"}
		]
	}`), 0o600))

	specs, err := upload.LoadManifestDoc(doc, nil)
	require.NoError(t, err)
	require.Len(t, specs, 2)
	assert.Equal(t, upload.RoleModel, specs[0].Role)
	assert.Equal(t, "m.onnx", specs[0].Filename)
	assert.Equal(t, 0, specs[1].InputIndex, "input_index defaults to input order")
	assert.NotEmpty(t, specs[1].CRC32C)
}

func TestLoadManifestDocRejectsBadVersionAndRoles(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "manifest.json")

	require.NoError(t, os.WriteFile(doc, []byte(`{"manifest_version": 1, "files": []}`), 0o600))
	_, err := upload.LoadManifestDoc(doc, nil)
	require.ErrorContains(t, err, "manifest_version")

	require.NoError(t, os.WriteFile(doc, []byte(`{"manifest_version": 2, "files": [{"path": "x", "role": "weights"}]}`), 0o600))
	_, err = upload.LoadManifestDoc(doc, nil)
	require.ErrorContains(t, err, "role")

	// exactly one model file required
	require.NoError(t, os.WriteFile(doc, []byte(`{"manifest_version": 2, "files": [{"path": "x", "role": "input"}]}`), 0o600))
	_, err = upload.LoadManifestDoc(doc, nil)
	require.ErrorContains(t, err, "model")
}

func jsonStr(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		if r == '"' || r == '\\' {
			b = append(b, '\\')
		}
		b = append(b, string(r)...)
	}
	return string(append(b, '"'))
}
