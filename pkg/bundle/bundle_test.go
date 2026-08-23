package bundle

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestBundleDirectoryLeavesNoTempTar is the regression test for the Windows
// release build. BundleDirectory writes an uncompressed .tar first, gzips it and
// then removes it -- but it used to hold the file's handle open across that
// removal. Unix allows unlinking an open file, so the bug was invisible here;
// Windows returns "The process cannot access the file because it is being used
// by another process" and the win64 job failed with it on tag v0.1.1.
//
// This assertion cannot fail on a Unix host either before or after the fix. It
// pins the contract -- no temp file survives a successful bundle -- so the
// invariant is stated somewhere other than a CI log.
func TestBundleDirectoryLeavesNoTempTar(t *testing.T) {
	outDir := t.TempDir()
	out := filepath.Join(outDir, "test-bundle.tar.gz")
	newSourceTree(t)

	params := &BundleRequestParams{Directory: "./", Output: out}
	t.Logf("input: dir=./ out=%s", out)

	res, err := BundleDirectory(params)
	if err != nil {
		t.Fatalf("BundleDirectory: %v", err)
	}
	t.Logf("output: %s", res.Output)

	left := listDir(t, outDir)
	t.Logf("output dir contains: %v", left)

	if _, err := os.Stat(out); err != nil {
		t.Errorf("expected the .tar.gz at %s: %v", out, err)
	}
	tempTar := out[:len(out)-3] // BundleDirectory strips ".gz" to name its temp file
	if _, err := os.Stat(tempTar); !os.IsNotExist(err) {
		t.Errorf("temp tar %s was not removed (stat err: %v)", tempTar, err)
	}
	if len(left) != 1 {
		t.Errorf("expected exactly one file in the output dir, got %v", left)
	}
}

// TestBundleDirectoryContents covers the three selection flags scripts/bundle.sh
// depends on: --ext and --files keep source and secrets out of a release
// archive, and --add puts the compiled binary at the archive root.
func TestBundleDirectoryContents(t *testing.T) {
	outDir := t.TempDir()
	out := filepath.Join(outDir, "test-bundle.tar.gz")

	binary := filepath.Join(t.TempDir(), "emule-http-cache")
	if err := os.WriteFile(binary, []byte("not really a binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	newSourceTree(t)

	params := &BundleRequestParams{
		Directory:       "./",
		Output:          out,
		ExcludeDirs:     []string{"skipme"},
		ExcludeFiles:    []string{"config.yaml"},
		ExcludeExt:      []string{".go"},
		ExcludeDotfiles: true,
		Files:           []string{binary},
	}
	t.Logf("input: dir=./ exclude=%v files=%v ext=%v add=%v",
		params.ExcludeDirs, params.ExcludeFiles, params.ExcludeExt, params.Files)

	if _, err := BundleDirectory(params); err != nil {
		t.Fatalf("BundleDirectory: %v", err)
	}

	got := manifest(t, out)
	t.Logf("output: archive contains %v", got)

	mustHave := []string{"emule-http-cache", "README.md", "scripts/update.sh"}
	for _, want := range mustHave {
		if !contains(got, want) {
			t.Errorf("expected %q in the archive, got %v", want, got)
		}
	}
	mustNotHave := []string{"config.yaml", "main.go", ".hidden", "skipme/junk.txt"}
	for _, unwanted := range mustNotHave {
		if contains(got, unwanted) {
			t.Errorf("did not expect %q in the archive, got %v", unwanted, got)
		}
	}
}

// TestBundleDirectoryOverwrites exercises the pre-existing-archive removal: a
// release is often bundled twice in a row over the same output path.
func TestBundleDirectoryOverwrites(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test-bundle.tar.gz")
	newSourceTree(t)

	for i := 1; i <= 2; i++ {
		res, err := BundleDirectory(&BundleRequestParams{Directory: "./", Output: out})
		if err != nil {
			t.Fatalf("BundleDirectory run %d: %v", i, err)
		}
		info, err := os.Stat(res.Output)
		if err != nil {
			t.Fatalf("stat run %d: %v", i, err)
		}
		t.Logf("run %d output: %s (%d bytes)", i, res.Output, info.Size())
	}
}

// newSourceTree builds a small directory that looks like the repo does to the
// bundler -- a doc, a script in a subdirectory, a source file, a secret and a
// dotfile -- and makes it the working directory.
//
// The chdir matters: BundleDirectory names archive entries from the paths
// filepath.Walk yields, so only a relative Directory produces the flat relative
// names a release archive needs. That is why scripts/bundle.sh passes --dir=./
// rather than an absolute path, and the tests drive it the same way.
func newSourceTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"README.md":         "# readme",
		"config.yaml":       "api_keys:\n  laptop:\n    secret: nope\n",
		"main.go":           "package main\n",
		".hidden":           "dotfile",
		"scripts/update.sh": "#!/bin/sh\n",
		"skipme/junk.txt":   "junk",
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Chdir(dir)
	return dir
}

// manifest returns the sorted slash-separated paths held in a .tar.gz.
func manifest(t *testing.T, archive string) []string {
	t.Helper()

	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()

	var names []string
	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		names = append(names, filepath.ToSlash(header.Name))
	}
	sort.Strings(names)
	return names
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
