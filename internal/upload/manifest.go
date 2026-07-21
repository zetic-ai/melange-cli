package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// FileSpec is one fully-digested manifest entry plus the local path it came
// from. Specs are ordered: model first, then inputs (InputIndex order), then
// external data; ClientFileID is "f<position>" in that order.
type FileSpec struct {
	ClientFileID string
	Path         string // local filesystem path
	Role         string
	InputIndex   int // -1 unless Role == RoleInput
	Filename     string
	Size         int64
	CRC32C       string
	SHA256       string
}

// NoteFunc observes a file about to be digested (used for "hashing large
// file" progress messages). size is from os.Stat, before the read pass.
type NoteFunc func(path string, size int64)

// localFile is a pre-digest manifest entry.
type localFile struct {
	path       string
	role       string
	inputIndex int
	filename   string
}

// BuildManifest digests the model, input, and external-data files into
// ordered manifest entries: model first, then inputs in the given order
// (input_index = position), then external data.
func BuildManifest(model string, inputs, external []string, note NoteFunc) ([]FileSpec, error) {
	files := []localFile{{path: model, role: RoleModel, inputIndex: -1}}
	for i, p := range inputs {
		files = append(files, localFile{path: p, role: RoleInput, inputIndex: i})
	}
	for _, p := range external {
		files = append(files, localFile{path: p, role: RoleExternalData, inputIndex: -1})
	}
	return digestAll(files, note)
}

// manifestDoc is the --input-manifest JSON shape: manifest v2 entries with a
// local "path" per file. Digests are always computed locally from the file
// contents, never trusted from the document.
type manifestDoc struct {
	ManifestVersion int               `json:"manifest_version"`
	Files           []manifestDocFile `json:"files"`
}

type manifestDocFile struct {
	Path       string `json:"path"`
	Role       string `json:"role"`
	InputIndex *int   `json:"input_index"`
	Filename   string `json:"filename"`
}

// LoadManifestDoc parses an --input-manifest document and digests its files.
// File order is preserved; inputs without an explicit input_index are
// numbered by their order of appearance among inputs.
func LoadManifestDoc(path string, note NoteFunc) ([]FileSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var doc manifestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}
	if doc.ManifestVersion != 2 {
		return nil, fmt.Errorf("manifest %s: manifest_version must be 2, got %d", path, doc.ManifestVersion)
	}

	var files []localFile
	models, nextInput := 0, 0
	seenIndex := map[int]bool{}
	for i, df := range doc.Files {
		if df.Path == "" {
			return nil, fmt.Errorf("manifest %s: files[%d] is missing \"path\"", path, i)
		}
		if !slices.Contains([]string{RoleModel, RoleInput, RoleExternalData}, df.Role) {
			return nil, fmt.Errorf("manifest %s: files[%d] has invalid role %q (expected model, input, or external_data)", path, i, df.Role)
		}
		lf := localFile{path: df.Path, role: df.Role, inputIndex: -1, filename: df.Filename}
		switch df.Role {
		case RoleModel:
			models++
		case RoleInput:
			if df.InputIndex != nil {
				lf.inputIndex = *df.InputIndex
			} else {
				lf.inputIndex = nextInput
			}
			if lf.inputIndex < 0 || seenIndex[lf.inputIndex] {
				return nil, fmt.Errorf("manifest %s: files[%d] has invalid or duplicate input_index %d", path, i, lf.inputIndex)
			}
			seenIndex[lf.inputIndex] = true
			nextInput = lf.inputIndex + 1
		}
		files = append(files, lf)
	}
	if models != 1 {
		return nil, fmt.Errorf("manifest %s: exactly one file with role \"model\" is required, found %d", path, models)
	}
	return digestAll(files, note)
}

// digestAll stats and digests every file, assigns f0..fN client ids in
// order, and rejects duplicate basenames and empty files.
func digestAll(files []localFile, note NoteFunc) ([]FileSpec, error) {
	specs := make([]FileSpec, 0, len(files))
	seen := map[string]string{} // basename -> first path
	for i, lf := range files {
		filename := lf.filename
		if filename == "" {
			filename = filepath.Base(lf.path)
		}
		if first, dup := seen[filename]; dup {
			return nil, fmt.Errorf("%w %q (%s and %s); rename one of the files",
				ErrDuplicateFilename, filename, first, lf.path)
		}
		seen[filename] = lf.path

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
		specs = append(specs, FileSpec{
			ClientFileID: fmt.Sprintf("f%d", i),
			Path:         lf.path,
			Role:         lf.role,
			InputIndex:   lf.inputIndex,
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
		return fmt.Sprintf("{tag}/inputs/%02d_%s", spec.InputIndex, spec.Filename)
	case RoleExternalData:
		return "{tag}/data/" + spec.Filename
	default:
		return "{tag}/" + spec.Filename
	}
}
