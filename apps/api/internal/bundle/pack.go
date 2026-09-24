package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Bundle is a bundle in memory: the manifest and every carried file, keyed by
// its slash-path inside the tar. Everything — building, packing, unpacking,
// verifying — goes through this one shape, so the bytes that are hashed and the
// bytes that are stored cannot drift apart.
type Bundle struct {
	Manifest Manifest
	Files    map[string][]byte
}

// maxEntry caps a single carried file. A bundle is a workflow and its skills —
// prose and a little code — so anything larger is a mistake, and an uncapped
// unpack of attacker-supplied bytes is how a tar becomes a memory exhaustion.
const maxEntry = 16 << 20

// New starts an empty bundle.
func New(kind, project, name, version string) *Bundle {
	return &Bundle{
		Manifest: Manifest{SchemaVersion: SchemaVersion, Kind: kind, Project: project, Name: name, Version: version},
		Files:    map[string][]byte{},
	}
}

// AddWorkflow carries the workflow document itself.
func (b *Bundle) AddWorkflow(yaml []byte) {
	b.Files[WorkflowPath] = yaml
	b.Manifest.Workflow = Entry{Name: b.Manifest.Name, Path: WorkflowPath, Digest: DigestBytes(yaml)}
}

// AddMcp carries the MCP declarations the steps name.
func (b *Bundle) AddMcp(doc []byte) {
	b.Files[McpPath] = doc
	b.Manifest.Mcp = &Entry{Name: "mcp", Path: McpPath, Digest: DigestBytes(doc)}
}

// AddSkillDir copies a resolved skill directory in verbatim and records its
// content digest — the whole point of publishing: the dependency travels, it is
// not a reference for the receiving machine to look up.
func (b *Bundle) AddSkillDir(name, dir string) error {
	root := path.Join(SkillsDir, name)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(raw) > maxEntry {
			return fmt.Errorf("skill %s: %s is larger than %d bytes", name, rel, maxEntry)
		}
		b.Files[path.Join(root, filepath.ToSlash(rel))] = raw
		return nil
	})
	if err != nil {
		return err
	}
	digest, lines, err := DigestDir(dir)
	if err != nil {
		return err
	}
	b.Manifest.Skills = append(b.Manifest.Skills, Entry{Name: name, Path: root, Digest: digest, Files: lines})
	sort.Slice(b.Manifest.Skills, func(i, j int) bool { return b.Manifest.Skills[i].Name < b.Manifest.Skills[j].Name })
	return nil
}

// listingUnder recomputes a directory entry's canonical listing from the bytes
// actually carried, by the same rule DirListing applies on disk. The two must
// agree or the digest means nothing.
func (b *Bundle) listingUnder(root string) []string {
	prefix := root + "/"
	var lines []string
	for p, raw := range b.Files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		lines = append(lines, DigestBytes(raw)+"  "+strings.TrimPrefix(p, prefix)+"\n")
	}
	sort.Strings(lines)
	return lines
}

// Verify recomputes every entry digest and the manifest digest from the bytes
// in hand, and reports the claimed digest it was handed disagreeing with them.
//
// This is the server's half of content addressing: a store that accepts bytes
// on the uploader's word is not content-addressed, it is merely named after a
// hash the uploader chose.
func (b *Bundle) Verify(claimed string) (string, error) {
	if b.Manifest.SchemaVersion != SchemaVersion {
		return "", fmt.Errorf("bundle: unknown manifest schema version %d", b.Manifest.SchemaVersion)
	}
	wf, ok := b.Files[WorkflowPath]
	if !ok {
		return "", errors.New("bundle: no " + WorkflowPath)
	}
	if got := DigestBytes(wf); got != b.Manifest.Workflow.Digest {
		return "", fmt.Errorf("bundle: %s digest mismatch: manifest says %s, content is %s", WorkflowPath, b.Manifest.Workflow.Digest, got)
	}
	if b.Manifest.Mcp != nil {
		doc, ok := b.Files[McpPath]
		if !ok {
			return "", errors.New("bundle: manifest names " + McpPath + " but it is not carried")
		}
		if got := DigestBytes(doc); got != b.Manifest.Mcp.Digest {
			return "", fmt.Errorf("bundle: %s digest mismatch: manifest says %s, content is %s", McpPath, b.Manifest.Mcp.Digest, got)
		}
	}
	for _, sk := range b.Manifest.Skills {
		got := DigestListing(b.listingUnder(sk.Path))
		if got != sk.Digest {
			return "", fmt.Errorf("bundle: skill %s digest mismatch: manifest says %s, content is %s", sk.Name, sk.Digest, got)
		}
	}
	digest, err := b.Manifest.Digest()
	if err != nil {
		return "", err
	}
	if claimed != "" && claimed != digest {
		return "", fmt.Errorf("bundle: manifest digest mismatch: claimed %s, computed %s", claimed, digest)
	}
	return digest, nil
}

// Pack writes the gzipped tar. The manifest goes in as its CANONICAL bytes, so
// what was hashed is what is stored.
func (b *Bundle) Pack() ([]byte, error) {
	canon, err := b.Manifest.Canonical()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(b.Files)+1)
	for p := range b.Files {
		names = append(names, p)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, raw []byte) error {
		// Mode and mtime are FIXED. A tar whose header carried them would give
		// two packs of the same content two different archives, and the point
		// of a digest is that it does not.
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw))}); err != nil {
			return err
		}
		_, err := tw.Write(raw)
		return err
	}
	if err := write(ManifestPath, canon); err != nil {
		return nil, err
	}
	for _, n := range names {
		if err := write(n, b.Files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unpack reads a gzipped tar back. It refuses any member whose name escapes the
// bundle root — the same rule blob.Folder applies to a key, for the same reason.
func Unpack(raw []byte) (*Bundle, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	b := &Bundle{Files: map[string][]byte{}}
	var manifest []byte
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(filepath.ToSlash(h.Name))
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("bundle: entry %q escapes the bundle", h.Name)
		}
		content, err := io.ReadAll(io.LimitReader(tr, maxEntry+1))
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		if len(content) > maxEntry {
			return nil, fmt.Errorf("bundle: entry %q is larger than %d bytes", name, maxEntry)
		}
		if name == ManifestPath {
			manifest = content
			continue
		}
		b.Files[name] = content
	}
	if manifest == nil {
		return nil, errors.New("bundle: no " + ManifestPath)
	}
	if err := jsonUnmarshalStrict(manifest, &b.Manifest); err != nil {
		return nil, fmt.Errorf("bundle: %s: %w", ManifestPath, err)
	}
	return b, nil
}

// Materialise writes the carried skills out as a SKILL ROOT: one directory
// holding exactly this bundle's skills, ready to be prepended to the registry's
// roots so first-root-wins resolves each step's `skills:` to the carried copy.
//
// This is the server's own work on a CLI-initiated publish or pull. It is never
// reachable from a tool handed to a step — skills/platform.go's boundary that a
// platform tool does not write stands untouched.
func (b *Bundle) Materialise(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	prefix := SkillsDir + "/"
	for p, raw := range b.Files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, prefix)))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}
