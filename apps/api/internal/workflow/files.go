package workflow

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FILES THAT TRAVEL WITH THE WORKFLOW
//
// A workflow is not always only YAML. `run: node seed.js` needs a seed.js, and
// writing that script into the YAML as a heredoc is the worst version of both
// things: unhighlighted, unlintable, undiffable.
//
// So a workflow may be a DIRECTORY instead of a file:
//
//	workflows/
//	  nightly.yaml            ← still fine, still loads, nothing changed
//	  report/
//	    workflow.yaml         ← the definition
//	    run.js                ← staged into the workspace; `run: node run.js`
//	    lib/format.js
//
// THERE IS NO `files:` KEY. Putting a file next to the workflow IS the
// declaration — the directory is the manifest. Nothing to keep in step, nothing
// to forget, and what you see in the repository is what the step gets.
//
// The two folder features are different things and stay different:
//
//	sidecar files   the workflow's OWN inputs, versioned with it, staged IN
//	mount           a folder that already exists on the machine, attached
//
// They cannot collide: a sidecar whose destination falls inside a mount point
// is refused at load time, so shipping a run.js can never overwrite something
// in somebody's data folder.

// DefinitionFile is the name a workflow directory's definition must have. One
// spelling, so a directory with two YAML files is not a guessing game.
const DefinitionFile = "workflow.yaml"

// File is one sidecar, carried by value so it can travel to a worker that
// cannot see this machine's disk.
type File struct {
	// Path is relative to the workflow directory, always slash-separated: it is
	// written out on a machine whose separator may not be this one's.
	Path string `json:"path"`
	Mode uint32 `json:"mode,omitempty"`
	Body []byte `json:"body"`
}

// THE LISTING SHOWS THE WHOLE WORKFLOW.
//
// A workflow is the YAML *and* everything sitting with it, so a listing that
// shows only the YAML is a listing you cannot reuse from: you copy it, and the
// first run fails on a script nobody told you about.
//
// Nothing here decides which files matter. A README, a fixture, a .sql the
// platform has no opinion about — all of it is listed, because the person or
// agent reading the listing is the one who knows what is relevant, and a
// platform that guesses can only guess wrong in the direction of hiding
// something.

// FileInfo is one sidecar as a LISTING shows it: where it is and how big, and
// no body. A gallery of twenty workflows should not ship twenty scripts to
// render twenty rows; the body is one fetch away, on the workflow itself.
type FileInfo struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	Mode uint32 `json:"mode,omitempty"`
}

// Infos is the listing view of a workflow's files.
func Infos(files []File) []FileInfo {
	if len(files) == 0 {
		return nil
	}
	out := make([]FileInfo, 0, len(files))
	for _, f := range files {
		out = append(out, FileInfo{Path: f.Path, Size: len(f.Body), Mode: f.Mode})
	}
	return out
}

// MarshalJSON adds `size` beside the body, so every place a workflow's files
// are serialised says how big they are — computed from the bytes rather than
// stored, which is the only way it cannot drift from them. Decoding is
// unchanged: `size` is derived, and an incoming one is ignored.
func (f File) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path string `json:"path"`
		Size int    `json:"size"`
		Mode uint32 `json:"mode,omitempty"`
		Body []byte `json:"body,omitempty"`
	}{f.Path, len(f.Body), f.Mode, f.Body})
}

// Clone deep-copies a workflow's files, so a COPY of a workflow never shares
// bytes with the workflow it came from. Two definitions aliasing one slice is
// the kind of thing that works until something edits one of them.
func Clone(files []File) []File {
	if len(files) == 0 {
		return nil
	}
	out := make([]File, len(files))
	for i, f := range files {
		out[i] = File{Path: f.Path, Mode: f.Mode, Body: append([]byte(nil), f.Body...)}
	}
	return out
}

// maxSidecarBytes and maxSidecarFiles cap what one workflow carries. A sidecar
// is a script or a fixture; a directory far past this is a mistake — a
// checked-in node_modules, a dataset that should have been a mount — and
// shipping it to every worker on every step is a slow way to find that out.
const (
	maxSidecarBytes = 4 << 20
	maxSidecarFiles = 200
)

// loadWorkflowDir reads a directory-form workflow: its definition plus every
// other file beside it.
func loadWorkflowDir(dir string) (raw []byte, defPath string, files []File, err error) {
	defPath = filepath.Join(dir, DefinitionFile)
	if !fileExists(defPath) {
		if alt := filepath.Join(dir, "workflow.yml"); fileExists(alt) {
			defPath = alt
		} else {
			return nil, "", nil, nil // not a workflow directory; ignored
		}
	}
	raw, err = os.ReadFile(defPath)
	if err != nil {
		return nil, "", nil, err
	}
	var total int
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if p == defPath {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		// A symlink is not followed and not carried. It would either dangle on
		// the worker or, worse, point at something outside the workflow that
		// the author never meant to ship.
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; a workflow's files are carried by value, so they must be real files", rel)
		}
		body, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		total += len(body)
		if total > maxSidecarBytes || len(files) >= maxSidecarFiles {
			return fmt.Errorf("the files beside this workflow exceed %d bytes or %d files — "+
				"a sidecar is a script or a fixture. A big folder belongs in `mount:`", maxSidecarBytes, maxSidecarFiles)
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		files = append(files, File{
			Path: filepath.ToSlash(rel),
			Mode: uint32(info.Mode().Perm()),
			Body: body,
		})
		return nil
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("%s: %w", dir, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return raw, defPath, files, nil
}

// CheckFiles is the author-time rule for sidecars. Each one is an EXECUTABLE
// PAYLOAD — that is the point of shipping a run.js, and it is also the risk —
// so where it lands is checked before anything is written anywhere.
//
//  1. The destination must stay inside the workspace: no absolute path, no
//     `..`, nothing that resolves out.
//  2. It must not land inside a mount point. A sidecar is the workflow's own
//     file and a mount is somebody's real folder; staging one over the other
//     would overwrite a user's data with a file from a repository.
func CheckFiles(where string, files []File, mounts []Mount) error {
	ats := MountAts(mounts)
	for _, f := range files {
		// A sidecar path is ALWAYS slash-separated, so a backslash is either a
		// Windows path that arrived over the wire or a pathological filename.
		// Refused rather than translated: on Linux `..\evil.js` is one harmless
		// filename and on Windows it is an escape, and a check that gives two
		// answers depending on where it runs is not a check.
		if strings.ContainsRune(f.Path, '\\') {
			return fmt.Errorf("%s: file %q — a workflow's file paths are written with forward slashes", where, f.Path)
		}
		clean := filepath.Clean(filepath.FromSlash(f.Path))
		switch {
		case clean == "." || clean == "":
			return fmt.Errorf("%s: a file with no name", where)
		case filepath.IsAbs(clean) || isWindowsAbsPath(clean):
			return fmt.Errorf("%s: file %q is an absolute path; a workflow's files are staged inside the workspace", where, f.Path)
		case clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)):
			return fmt.Errorf("%s: file %q climbs out of the workspace", where, f.Path)
		}
		slash := filepath.ToSlash(clean)
		for _, at := range ats {
			if slash == at || strings.HasPrefix(slash, at+"/") {
				return fmt.Errorf("%s: file %q would be staged inside the mount at %q. "+
					"A workflow's own files never overwrite an attached folder — move it, or change where the mount lands", where, f.Path, at)
			}
		}
	}
	return nil
}
