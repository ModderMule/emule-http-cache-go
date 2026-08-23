package bundle

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// TestBundleDirectoryWritesSlashSeparatedNames is the regression test for the
// v0.1.2 win64 release, whose archive stored "docs\architecture.md" and
// "http_public\static\tpl\page.gohtml" instead of paths. Tar names are always
// slash-separated whatever the host separator is, so an archive built on Windows
// extracts to the same tree as one built anywhere else; without that, tar on
// macOS and Linux produces flat files with backslashes in their names rather
// than a directory tree at all.
//
// Like TestBundleDirectoryLeavesNoTempTar, this cannot fail on a Unix host
// either before or after the fix -- filepath.Join already emits "/" here. It is
// a gate for the Windows runner. What is checkable on any host is whether the
// gate works at all, which is what TestBadTarNamesCatchesWindowsSeparators
// covers; the final proof is the published win64 manifest.
func TestBundleDirectoryWritesSlashSeparatedNames(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test-bundle.tar.gz")

	binary := filepath.Join(t.TempDir(), "emule-http-cache")
	if err := os.WriteFile(binary, []byte("not really a binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	newSourceTree(t)

	params := &BundleRequestParams{
		Directory:       "./",
		Output:          out,
		ExcludeDotfiles: true,
		Files:           []string{binary},
	}
	t.Logf("input: dir=./ out=%s add=%v (host separator %q)", out, params.Files, string(filepath.Separator))

	if _, err := BundleDirectory(params); err != nil {
		t.Fatalf("BundleDirectory: %v", err)
	}

	got := manifest(t, out)
	t.Logf("output: archive contains %v", got)

	if bad := badTarNames(got); len(bad) != 0 {
		t.Errorf("archive holds malformed tar names %v (full manifest %v)", bad, got)
	}

	// The nested entry is the one that carries a separator at all, so a manifest
	// without it would pass the check above while proving nothing.
	if !contains(got, "scripts/update.sh") {
		t.Errorf("expected a nested entry \"scripts/update.sh\" to exercise the separator, got %v", got)
	}
}

// TestBadTarNamesCatchesWindowsSeparators checks the guard the test above leans
// on. That test cannot fail on this host, so its value rests entirely on
// badTarNames actually rejecting a Windows-shaped name and on manifest handing
// over the stored names unaltered -- both of which are host-independent, and
// both of which have already been wrong here once.
func TestBadTarNamesCatchesWindowsSeparators(t *testing.T) {
	cases := []struct {
		name    string
		wantBad bool
	}{
		{"README.md", false},
		{"scripts/update.sh", false},
		{"http_public/static/tpl/page.gohtml", false},
		{`docs\architecture.md`, true},               // what v0.1.2 win64 shipped
		{`http_public\static\tpl\page.gohtml`, true}, // and its nested form
		{"/etc/passwd", true},                        // absolute
		{"../outside.txt", true},                     // escapes the extract dir
		{"docs/../../outside.txt", true},             // and the embedded form
		{`C:\dist\emule-http-cache.exe`, true},       // drive-qualified
	}
	for _, tc := range cases {
		bad := len(badTarNames([]string{tc.name})) != 0
		t.Logf("input %-40q -> bad=%v (want %v)", tc.name, bad, tc.wantBad)
		if bad != tc.wantBad {
			t.Errorf("badTarNames(%q) reported bad=%v, want %v", tc.name, bad, tc.wantBad)
		}
	}

	// And prove manifest reports what is stored rather than normalising it: this
	// archive is built by hand precisely because BundleDirectory cannot produce a
	// backslash name on this host.
	archive := filepath.Join(t.TempDir(), "handmade.tar.gz")
	writeArchiveWithNames(t, archive, []string{"README.md", `docs\architecture.md`})

	got := manifest(t, archive)
	t.Logf("hand-built archive input: %v", []string{"README.md", `docs\architecture.md`})
	t.Logf("manifest output: %v", got)

	if !contains(got, `docs\architecture.md`) {
		t.Errorf("manifest normalised the stored name away, got %v -- the Windows gate would not fire", got)
	}
	if bad := badTarNames(got); len(bad) != 1 {
		t.Errorf("expected badTarNames to flag exactly the backslash entry, got %v", bad)
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

// manifest returns the sorted entry names held in a .tar.gz, exactly as they are
// stored.
//
// Deliberately no filepath.ToSlash on the way out. It used to normalise here,
// which laundered away the one defect these tests most need to see: on a Windows
// runner an archive holding "scripts\update.sh" would have satisfied
// TestBundleDirectoryContents' check for "scripts/update.sh". Raw names mean
// that test doubles as a separator gate.
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
		names = append(names, header.Name)
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

// badTarNames returns the entries that are not well-formed tar names. A tar name
// is slash-separated and relative to the archive root, so a backslash, a leading
// slash, a drive letter or a ".." segment all mean the archive will not extract
// to the tree it was meant to describe.
//
// Note this works on raw strings rather than path/filepath: the point is to
// catch names carrying the *other* host's separator, and filepath on Unix
// happily treats a backslash as an ordinary character.
func badTarNames(names []string) []string {
	var bad []string
	for _, name := range names {
		switch {
		case strings.Contains(name, `\`):
		case strings.HasPrefix(name, "/"):
		case len(name) > 1 && name[1] == ':':
		case name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || strings.HasSuffix(name, "/.."):
		default:
			continue
		}
		bad = append(bad, name)
	}
	return bad
}

// writeArchiveWithNames builds a .tar.gz whose entries carry exactly the given
// names. BundleDirectory cannot emit a backslash name on a Unix host, so a
// hand-built archive is the only way to check that the tests would notice one.
func writeArchiveWithNames(t *testing.T, archive string, names []string) {
	t.Helper()

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()

	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	for _, name := range names {
		body := []byte("body of " + name)
		header := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
