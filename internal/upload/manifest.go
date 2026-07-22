package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Manifest file roles (the server-side ManifestFile.role enum).
const (
	RoleModel        = "model"
	RoleInput        = "input"
	RoleExternalData = "external_data"
)

// ErrDuplicateFilename reports two manifest files sharing a basename, which
// would collide in the server's canonical layout.
var ErrDuplicateFilename = errors.New("duplicate filename")

// ErrInvalidManifest marks a locally-detectable manifest contract violation.
// Commands map it to a usage error (exit 2); filesystem read failures remain
// operational errors (exit 1).
var ErrInvalidManifest = errors.New("invalid upload manifest")

func invalidManifestf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
}

// FileSpec is one fully-digested manifest entry plus the local path it came
// from. Specs are ordered: model first, then inputs (InputIndex order), then
// external data; ClientFileID is "f<position>" in that order.
type FileSpec struct {
	ClientFileID string
	Path         string // local filesystem path
	Role         string
	InputIndex   int  // -1 unless Role == RoleInput
	BucketIndex  *int // nil unless this is a bucketed input
	Filename     string
	Size         int64
	CRC32C       string
	SHA256       string
}

// BucketSpec is one bounded .pt2 input-shape declaration. Every bucket in a
// manifest carries the same complete set of input indexes.
type BucketSpec struct {
	Index int   `json:"index"`
	Dims  []int `json:"dims"`
}

const (
	maxBuckets    = 16
	maxTensorRank = 8
	maxDimension  = 1<<31 - 1
)

// NoteFunc observes a file about to be digested (used for "hashing large
// file" progress messages). size is from os.Stat, before the read pass.
type NoteFunc func(path string, size int64)

// localFile is a pre-digest manifest entry.
type localFile struct {
	path        string
	role        string
	inputIndex  int
	bucketIndex int
	filename    string
}

// BuildManifest digests the model, input, and external-data files into
// ordered manifest entries: model first, then inputs in the given order
// (input_index = position), then external data.
func BuildManifest(model string, inputs, external []string, note NoteFunc) ([]FileSpec, error) {
	files := []localFile{{path: model, role: RoleModel, inputIndex: -1, bucketIndex: -1}}
	for i, p := range inputs {
		files = append(files, localFile{path: p, role: RoleInput, inputIndex: i, bucketIndex: -1})
	}
	for _, p := range external {
		files = append(files, localFile{path: p, role: RoleExternalData, inputIndex: -1, bucketIndex: -1})
	}
	if err := validateLocalFiles(files); err != nil {
		return nil, err
	}
	return digestAll(files, note)
}

// BuildBucketedManifest maps --input files to buckets in declaration order,
// with an equal input arity per bucket, then emits stable bucket/index order.
func BuildBucketedManifest(model string, inputs, external []string, buckets []BucketSpec, note NoteFunc) ([]FileSpec, error) {
	if err := validateBuckets(buckets); err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(model), ".pt2") {
		return nil, invalidManifestf("input buckets are only supported for .pt2 models")
	}
	if len(inputs) == 0 || len(inputs)%len(buckets) != 0 {
		return nil, invalidManifestf(
			"every bucket must provide the same number of inputs (got %d inputs for %d buckets)",
			len(inputs), len(buckets))
	}
	arity := len(inputs) / len(buckets)
	grouped := make(map[int][]string, len(buckets))
	for position, bucket := range buckets {
		grouped[bucket.Index] = inputs[position*arity : (position+1)*arity]
	}
	orderedBuckets := slices.Clone(buckets)
	slices.SortFunc(orderedBuckets, func(a, b BucketSpec) int { return a.Index - b.Index })
	files := []localFile{{path: model, role: RoleModel, inputIndex: -1, bucketIndex: -1}}
	for _, bucket := range orderedBuckets {
		for inputIndex, path := range grouped[bucket.Index] {
			files = append(files, localFile{
				path: path, role: RoleInput, inputIndex: inputIndex, bucketIndex: bucket.Index,
			})
		}
	}
	for _, path := range external {
		files = append(files, localFile{path: path, role: RoleExternalData, inputIndex: -1, bucketIndex: -1})
	}
	if err := validateLocalFiles(files); err != nil {
		return nil, err
	}
	return digestAll(files, note)
}

func validateBuckets(buckets []BucketSpec) error {
	if len(buckets) == 0 || len(buckets) > maxBuckets {
		return invalidManifestf("between 1 and %d buckets are required", maxBuckets)
	}
	seen := make(map[int]bool, len(buckets))
	for _, bucket := range buckets {
		if bucket.Index < 0 || bucket.Index >= maxBuckets || seen[bucket.Index] {
			return invalidManifestf("bucket index %d must be unique and between 0 and %d", bucket.Index, maxBuckets-1)
		}
		seen[bucket.Index] = true
		if len(bucket.Dims) == 0 || len(bucket.Dims) > maxTensorRank {
			return invalidManifestf("bucket %d dims must contain 1-%d positive integers", bucket.Index, maxTensorRank)
		}
		for _, dim := range bucket.Dims {
			if dim <= 0 || dim > maxDimension {
				return invalidManifestf("bucket %d dims must contain 1-%d positive integers", bucket.Index, maxTensorRank)
			}
		}
	}
	return nil
}

// manifestDoc is the --input-manifest JSON shape: manifest v2 entries with a
// local "path" per file. Digests are always computed locally from the file
// contents, never trusted from the document.
type manifestDoc struct {
	ManifestVersion int                 `json:"manifest_version"`
	Files           []manifestDocFile   `json:"files"`
	Options         *manifestDocOptions `json:"options"`
}

type manifestDocOptions struct {
	Buckets []BucketSpec `json:"buckets"`
}

type manifestDocFile struct {
	Path        string `json:"path"`
	Role        string `json:"role"`
	InputIndex  *int   `json:"input_index"`
	BucketIndex *int   `json:"bucket_index"`
	Filename    string `json:"filename"`
}

// LoadManifestDoc parses an --input-manifest document and digests its files.
// File order is preserved; inputs without an explicit input_index are
// numbered by their order of appearance among inputs.
func LoadManifestDoc(path string, note NoteFunc) ([]FileSpec, error) {
	specs, _, err := LoadManifestDocV2(path, note)
	return specs, err
}

// LoadManifestDocV2 also returns bucket options needed for the create body.
func LoadManifestDocV2(path string, note NoteFunc) ([]FileSpec, []BucketSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest: %w", err)
	}
	var doc manifestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, invalidManifestf("parsing manifest %s: %v", path, err)
	}
	if doc.ManifestVersion != 2 {
		return nil, nil, invalidManifestf("manifest %s: manifest_version must be 2, got %d", path, doc.ManifestVersion)
	}
	buckets := []BucketSpec(nil)
	if doc.Options != nil {
		buckets = doc.Options.Buckets
		if err := validateBuckets(buckets); err != nil {
			return nil, nil, fmt.Errorf("manifest %s: %w", path, err)
		}
	}

	var files []localFile
	manifestDir := filepath.Dir(path)
	models := 0
	nextInput := map[int]int{}
	seenIndex := map[int]map[int]bool{}
	for i, df := range doc.Files {
		if df.Path == "" {
			return nil, nil, invalidManifestf("manifest %s: files[%d] is missing \"path\"", path, i)
		}
		if !slices.Contains([]string{RoleModel, RoleInput, RoleExternalData}, df.Role) {
			return nil, nil, invalidManifestf("manifest %s: files[%d] has invalid role %q (expected model, input, or external_data)", path, i, df.Role)
		}
		if df.Role != RoleInput && df.InputIndex != nil {
			return nil, nil, invalidManifestf("manifest %s: files[%d].input_index is only valid for role \"input\"", path, i)
		}
		if df.Role != RoleInput && df.BucketIndex != nil {
			return nil, nil, invalidManifestf("manifest %s: files[%d].bucket_index is only valid for role \"input\"", path, i)
		}
		localPath := df.Path
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(manifestDir, localPath)
		}
		lf := localFile{path: localPath, role: df.Role, inputIndex: -1, bucketIndex: -1, filename: df.Filename}
		switch df.Role {
		case RoleModel:
			models++
		case RoleInput:
			bucket := -1
			if df.BucketIndex != nil {
				bucket = *df.BucketIndex
			}
			lf.bucketIndex = bucket
			if len(buckets) > 0 && df.BucketIndex == nil {
				return nil, nil, invalidManifestf("manifest %s: files[%d].bucket_index is required when options.buckets is declared", path, i)
			}
			if len(buckets) == 0 && df.BucketIndex != nil {
				return nil, nil, invalidManifestf("manifest %s: files[%d].bucket_index requires options.buckets", path, i)
			}
			if seenIndex[bucket] == nil {
				seenIndex[bucket] = map[int]bool{}
			}
			if df.InputIndex != nil {
				lf.inputIndex = *df.InputIndex
			} else {
				lf.inputIndex = nextInput[bucket]
			}
			if lf.inputIndex < 0 || seenIndex[bucket][lf.inputIndex] {
				return nil, nil, invalidManifestf("manifest %s: files[%d] has invalid or duplicate input_index %d", path, i, lf.inputIndex)
			}
			seenIndex[bucket][lf.inputIndex] = true
			nextInput[bucket] = lf.inputIndex + 1
		}
		files = append(files, lf)
	}
	if models != 1 {
		return nil, nil, invalidManifestf("manifest %s: exactly one file with role \"model\" is required, found %d", path, models)
	}
	if err := validateLocalFiles(files); err != nil {
		return nil, nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	if err := validateManifestBucketSets(files, buckets); err != nil {
		return nil, nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	for bucket, indexesSet := range seenIndex {
		indexes := make([]int, 0, len(indexesSet))
		for index := range indexesSet {
			indexes = append(indexes, index)
		}
		slices.Sort(indexes)
		for expected, actual := range indexes {
			if actual != expected {
				return nil, nil, invalidManifestf("manifest %s: input_index values within bucket %d must be contiguous starting at 0 (got %v)", path, bucket, indexes)
			}
		}
	}
	specs, err := digestAll(files, note)
	return specs, buckets, err
}

func validateManifestBucketSets(files []localFile, buckets []BucketSpec) error {
	if len(buckets) == 0 {
		return nil
	}
	declared := make(map[int]bool, len(buckets))
	for _, bucket := range buckets {
		declared[bucket.Index] = true
	}
	counts := make(map[int]int, len(buckets))
	for _, file := range files {
		if file.role == RoleInput {
			if !declared[file.bucketIndex] {
				return invalidManifestf("input references undeclared bucket %d", file.bucketIndex)
			}
			counts[file.bucketIndex]++
		}
	}
	want := -1
	for _, bucket := range buckets {
		if counts[bucket.Index] == 0 {
			return invalidManifestf("bucket %d has no input sample set", bucket.Index)
		}
		if want == -1 {
			want = counts[bucket.Index]
		} else if counts[bucket.Index] != want {
			return invalidManifestf("every bucket must provide the same number of inputs")
		}
	}
	modelName := ""
	for _, file := range files {
		if file.role == RoleModel {
			modelName = strings.ToLower(file.filename)
			if modelName == "" {
				modelName = strings.ToLower(filepath.Base(file.path))
			}
		}
	}
	if !strings.HasSuffix(modelName, ".pt2") {
		return invalidManifestf("input buckets are only supported for .pt2 models")
	}
	return nil
}

// validateLocalFiles mirrors the server's cross-field manifest invariants so
// --dry-run is a faithful preflight rather than a weaker client-only format.
func validateLocalFiles(files []localFile) error {
	modelName := ""
	hasExternal := false
	seen := map[string]string{}
	for _, lf := range files {
		name := lf.filename
		if name == "" {
			name = filepath.Base(lf.path)
		}
		if name == "" || name == "." || name == ".." {
			return invalidManifestf("invalid filename %q", name)
		}
		if strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
			return invalidManifestf("filename %q must be a basename without path separators", name)
		}
		if utf8.RuneCountInString(name) > 255 {
			return invalidManifestf("filename %q exceeds 255 characters", name)
		}
		folded := strings.ToLower(name)
		if first, ok := seen[folded]; ok {
			return fmt.Errorf("%w %q (%s and %s), compared case-insensitively; rename one of the files",
				ErrDuplicateFilename, name, first, lf.path)
		}
		seen[folded] = lf.path
		switch lf.role {
		case RoleModel:
			modelName = folded
		case RoleExternalData:
			hasExternal = true
		}
	}
	if hasExternal && !strings.HasSuffix(modelName, ".onnx") {
		return invalidManifestf("external_data files are only supported for .onnx models")
	}
	return nil
}

// digestAll stats and digests every file, assigns f0..fN client ids in
// order, and rejects duplicate basenames and empty files.
func digestAll(files []localFile, note NoteFunc) ([]FileSpec, error) {
	specs := make([]FileSpec, 0, len(files))
	seen := map[string]string{} // case-folded basename -> first path
	for i, lf := range files {
		filename := lf.filename
		if filename == "" {
			filename = filepath.Base(lf.path)
		}
		key := strings.ToLower(filename)
		if first, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w %q (%s and %s); rename one of the files",
				ErrDuplicateFilename, filename, first, lf.path)
		}
		seen[key] = lf.path

		fi, err := os.Stat(lf.path)
		if err != nil {
			return nil, err
		}
		if fi.Size() == 0 {
			return nil, fmt.Errorf("%s is empty; refusing to upload a 0-byte file", lf.path)
		}
		if note != nil {
			note(lf.path, fi.Size())
		}
		d, err := DigestFile(lf.path)
		if err != nil {
			return nil, err
		}
		var bucketIndex *int
		if lf.bucketIndex >= 0 {
			value := lf.bucketIndex
			bucketIndex = &value
		}
		specs = append(specs, FileSpec{
			ClientFileID: fmt.Sprintf("f%d", i),
			Path:         lf.path,
			Role:         lf.role,
			InputIndex:   lf.inputIndex,
			BucketIndex:  bucketIndex,
			Filename:     filename,
			Size:         d.Size,
			CRC32C:       d.CRC32C,
			SHA256:       d.SHA256,
		})
	}
	return specs, nil
}

// CanonicalPathPreview returns the relative destination the server will
// assign for spec, with the server-chosen tag as a "{tag}" placeholder:
// model files land at {tag}/<name>, external data at {tag}/data/<name>, and
// inputs at {tag}/inputs/<NN>_<name> (two-digit zero-padded input index).
func CanonicalPathPreview(spec FileSpec) string {
	switch spec.Role {
	case RoleInput:
		if spec.BucketIndex != nil {
			return fmt.Sprintf("{tag}/inputs/bucket_%d/%02d_%s", *spec.BucketIndex, spec.InputIndex, spec.Filename)
		}
		return fmt.Sprintf("{tag}/inputs/%02d_%s", spec.InputIndex, spec.Filename)
	case RoleExternalData:
		return "{tag}/data/" + spec.Filename
	default:
		return "{tag}/" + spec.Filename
	}
}
