package upload_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

func writeNamedFile(t *testing.T, dir, name, data string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}

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
	input := filepath.Join(dir, "input.bin")
	require.NoError(t, os.WriteFile(model, nil, 0o600))
	require.NoError(t, os.WriteFile(input, []byte("input"), 0o600))
	_, err := upload.BuildManifest(model, []string{input}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestBuildManifestAllowsModelOnly(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.onnx")
	require.NoError(t, os.WriteFile(model, []byte("model"), 0o600))

	specs, err := upload.BuildManifest(model, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, specs, 1)
	assert.Equal(t, upload.RoleModel, specs[0].Role)
	assert.Equal(t, "model.onnx", specs[0].Filename)
}

func TestBuildBucketedManifestOrdersCompleteSampleSets(t *testing.T) {
	dir := t.TempDir()
	model := writeNamedFile(t, dir, "model.pt2", "model")
	paths := []string{
		writeNamedFile(t, dir, "b1_x.npy", "b1x"),
		writeNamedFile(t, dir, "b1_y.npy", "b1y"),
		writeNamedFile(t, dir, "b0_x.npy", "b0x"),
		writeNamedFile(t, dir, "b0_y.npy", "b0y"),
	}
	buckets := []upload.BucketSpec{
		{Index: 1, Dims: []int{1, 3, 384, 384}},
		{Index: 0, Dims: []int{1, 3, 224, 224}},
	}

	specs, err := upload.BuildBucketedManifest(model, paths, nil, buckets, nil)
	require.NoError(t, err)
	require.Len(t, specs, 5)
	require.Nil(t, specs[0].BucketIndex)
	assert.Equal(t, []int{0, 0, 1, 1}, []int{
		*specs[1].BucketIndex, *specs[2].BucketIndex,
		*specs[3].BucketIndex, *specs[4].BucketIndex,
	})
	assert.Equal(t, []int{0, 1, 0, 1}, []int{
		specs[1].InputIndex, specs[2].InputIndex,
		specs[3].InputIndex, specs[4].InputIndex,
	})
	assert.Equal(t, "{tag}/inputs/bucket_1/01_b1_y.npy", upload.CanonicalPathPreview(specs[4]))
}

func TestBuildBucketedManifestRejectsUnevenOrInvalidBuckets(t *testing.T) {
	dir := t.TempDir()
	model := writeNamedFile(t, dir, "model.pt2", "model")
	inputs := []string{
		writeNamedFile(t, dir, "a.npy", "a"),
		writeNamedFile(t, dir, "b.npy", "b"),
		writeNamedFile(t, dir, "c.npy", "c"),
	}

	_, err := upload.BuildBucketedManifest(model, inputs, nil, []upload.BucketSpec{
		{Index: 0, Dims: []int{1}}, {Index: 1, Dims: []int{2}},
	}, nil)
	require.ErrorContains(t, err, "same number of inputs")

	_, err = upload.BuildBucketedManifest(model, inputs[:2], nil, []upload.BucketSpec{
		{Index: 0, Dims: []int{1, -1}}, {Index: 1, Dims: []int{2}},
	}, nil)
	require.ErrorContains(t, err, "positive integers")
}

func TestBuildManifestRejectsExternalDataForNonONNXModel(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.pt2")
	input := filepath.Join(dir, "input.bin")
	external := filepath.Join(dir, "weights.data")
	for _, path := range []string{model, input, external} {
		require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	}

	_, err := upload.BuildManifest(model, []string{input}, []string{external}, nil)
	require.ErrorContains(t, err, "only supported for .onnx")
}

func TestLoadManifestDocRejectsNonContiguousInputIndices(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.onnx")
	input := filepath.Join(dir, "i.bin")
	for _, path := range []string{model, input} {
		require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	}
	doc := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(doc, []byte(`{"manifest_version":2,"files":[`+
		`{"path":`+jsonStr(model)+`,"role":"model"},`+
		`{"path":`+jsonStr(input)+`,"role":"input","input_index":1}]}`), 0o600))

	_, err := upload.LoadManifestDoc(doc, nil)
	require.ErrorContains(t, err, "contiguous")
}

func TestLoadManifestDocRejectsInputIndexOnNonInput(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.onnx")
	input := filepath.Join(dir, "i.bin")
	for _, path := range []string{model, input} {
		require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	}
	doc := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(doc, []byte(`{"manifest_version":2,"files":[`+
		`{"path":`+jsonStr(model)+`,"role":"model","input_index":0},`+
		`{"path":`+jsonStr(input)+`,"role":"input","input_index":0}]}`), 0o600))

	_, err := upload.LoadManifestDoc(doc, nil)
	require.ErrorContains(t, err, "only valid for role")
}

func TestLoadManifestDocRejectsUnsafeAndCaseCollidingFilenames(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.onnx")
	input := filepath.Join(dir, "i.bin")
	for _, path := range []string{model, input} {
		require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	}

	tests := []struct {
		name      string
		modelName string
		inputName string
		want      string
	}{
		{"separator", "sub/model.onnx", "input.bin", "basename"},
		{"dot", ".", "input.bin", "invalid filename"},
		{"case collision", "MODEL.ONNX", "model.onnx", "case-insensitive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := filepath.Join(dir, tt.name+".json")
			body := `{"manifest_version":2,"files":[` +
				`{"path":` + jsonStr(model) + `,"role":"model","filename":` + jsonStr(tt.modelName) + `},` +
				`{"path":` + jsonStr(input) + `,"role":"input","input_index":0,"filename":` + jsonStr(tt.inputName) + `}]}`
			require.NoError(t, os.WriteFile(doc, []byte(body), 0o600))
			_, err := upload.LoadManifestDoc(doc, nil)
			require.ErrorContains(t, err, tt.want)
		})
	}
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

func TestLoadManifestDocResolvesRelativePathsFromManifestDirectory(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	require.NoError(t, os.Mkdir(filesDir, 0o700))
	model := writeNamedFile(t, filesDir, "model.onnx", "model")
	input := writeNamedFile(t, filesDir, "input.npy", "input")
	manifest := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(manifest, []byte(`{
		"manifest_version": 2,
		"files": [
			{"path": "files/model.onnx", "role": "model"},
			{"path": "files/input.npy", "role": "input"}
		]
	}`), 0o600))

	specs, err := upload.LoadManifestDoc(manifest, nil)
	require.NoError(t, err)
	require.Len(t, specs, 2)
	assert.Equal(t, model, specs[0].Path)
	assert.Equal(t, input, specs[1].Path)
}

func TestLoadManifestDocWithBuckets(t *testing.T) {
	dir := t.TempDir()
	model := writeNamedFile(t, dir, "m.pt2", "model")
	b0 := writeNamedFile(t, dir, "b0.npy", "b0")
	b1 := writeNamedFile(t, dir, "b1.npy", "b1")
	doc := filepath.Join(dir, "manifest.json")
	body := fmt.Sprintf(`{
  "manifest_version": 2,
  "options": {"buckets": [{"index": 0, "dims": [1]}, {"index": 1, "dims": [2]}]},
  "files": [
    {"path": %s, "role": "model"},
    {"path": %s, "role": "input", "bucket_index": 0},
    {"path": %s, "role": "input", "bucket_index": 1}
  ]
}`, jsonStr(model), jsonStr(b0), jsonStr(b1))
	require.NoError(t, os.WriteFile(doc, []byte(body), 0o600))

	specs, buckets, err := upload.LoadManifestDocV2(doc, nil)
	require.NoError(t, err)
	assert.Equal(t, []upload.BucketSpec{{Index: 0, Dims: []int{1}}, {Index: 1, Dims: []int{2}}}, buckets)
	require.NotNil(t, specs[1].BucketIndex)
	require.NotNil(t, specs[2].BucketIndex)
	assert.Equal(t, 0, *specs[1].BucketIndex)
	assert.Equal(t, 1, *specs[2].BucketIndex)
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
