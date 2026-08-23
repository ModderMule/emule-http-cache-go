// Package bundle creates a deployable tar.gz archive of a directory, excluding
// source files and other build-time artifacts. It is the standalone equivalent
// of verified-gateway's app/bundle.go.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// BundleRequestParams configures a bundle operation.
type BundleRequestParams struct {
	Directory string // the root directory to include files recursively in the bundle
	Output    string // the file and path where to write the bundle (.tar.gz appended if missing)

	// optional params
	ExcludeDirs     []string // (relative) directory names to exclude
	ExcludeFiles    []string // file names to exclude (supports a single "*" wildcard)
	ExcludeExt      []string // file extensions to exclude
	ExcludeDotfiles bool     // don't add dotfiles (.gitignore, ...)
	Files           []string // additional files (from other or excluded directories) to add to the root
}

// BundleResponse reports the result of a bundle operation.
type BundleResponse struct {
	Output string // the full path to the created bundle
}

func validate(params *BundleRequestParams) error {
	if params == nil {
		return errors.New("bundle params are required")
	}
	if params.Directory == "" {
		return errors.New("bundle directory is required")
	}
	if params.Output == "" {
		return errors.New("bundle output is required")
	}
	return nil
}

// BundleDirectory creates a tar.gz bundle of the given directory.
func BundleDirectory(params *BundleRequestParams) (*BundleResponse, error) {
	if err := validate(params); err != nil {
		return nil, err
	}

	hasBundleExt, _ := regexp.Match("\\.tar\\.gz$", []byte(params.Output))
	if !hasBundleExt {
		params.Output += ".tar.gz"
	}
	excludeDirs := createSetFromSlice(params.ExcludeDirs)
	for i := 0; i < len(params.ExcludeExt); i++ {
		curExt := params.ExcludeExt[i]
		if len(curExt) == 0 || curExt[:1] != "." { // ensure all ext start with dots
			params.ExcludeExt[i] = "." + curExt
		}
	}

	var excludeFilesByName []string
	var excludeFilesByRegex []string
	for _, name := range params.ExcludeFiles {
		if strings.Contains(name, "*") {
			excludeFilesByRegex = append(excludeFilesByRegex, name)
		} else {
			excludeFilesByName = append(excludeFilesByName, name)
		}
	}
	excludeFiles := createSetFromSlice(excludeFilesByName)
	for i, name := range excludeFilesByRegex {
		// first remove the star, then escape and at last add the (unescaped) "any char" match
		excludeFilesByRegex[i] = "^" + strings.Replace(regexp.QuoteMeta(strings.Replace(name, "*", ";;TEMP;;;", 1)), ";;TEMP;;;", ".+", 1) + "$"
	}

	excludeExt := createSetFromSlice(params.ExcludeExt)

	// Create a temp tar file and add some files to the archive
	tarFileName := params.Output[0 : len(params.Output)-3]
	file, err := os.Create(tarFileName)
	if err != nil {
		return nil, errors.Wrap(err, "can not create tar file")
	}
	defer file.Close()
	tw := tar.NewWriter(file)

	baseDir := filepath.Base(params.Directory)
	skipDir := func(path string, name string) error {
		if len(name) != 0 {
			if _, exists := excludeDirs[name]; exists { // skip this dir name
				return filepath.SkipDir
			} else if params.ExcludeDotfiles && name[:1] == "." && path != baseDir && path != params.Directory {
				return filepath.SkipDir
			}
		}
		return nil // go into the directory
	}

	// don't include our archive files
	//
	// Compared in slash form on both sides: tarFileName comes from --out, which
	// is written with forward slashes, while Walk yields the host separator. On
	// Windows the two could never match, so the temp .tar was only kept out of
	// its own archive by bundle.sh also passing --exclude=dist.
	tarFileRelative := strings.TrimPrefix(filepath.ToSlash(tarFileName), baseDir)
	if len(tarFileRelative) != 0 && tarFileRelative[:1] == "/" { // "path" in Walk starts without leading slash
		tarFileRelative = tarFileRelative[1:]
	}
	if err := os.Remove(params.Output); err != nil && !os.IsNotExist(err) {
		return nil, errors.Wrap(err, "can not remove pre-existing .tar.gz archive")
	}

	// list all files and add them to a tar archive
	err = filepath.Walk(params.Directory, func(path string, info os.FileInfo, err error) error {
		if err != nil { // swallow errors while iterating through dirs
			log.Printf("Error reading file to bundle: %s %+v", path, err)
			return nil // or return the error to abort
		}

		// skip symlinks
		if info.Mode() == os.ModeSymlink {
			return nil
		}

		// skip the file we are writing right now
		if filepath.ToSlash(path) == tarFileRelative {
			return nil
		}

		name := filepath.Base(path)
		if info.IsDir() {
			if err := skipDir(path, name); err != nil {
				return err
			}
			return nil // Walk() gets called for each file in that directory. Then we add it to our archive
		} else if !info.Mode().IsRegular() {
			return nil
		} else {
			ext := filepath.Ext(path)
			if len(ext) != 0 {
				if _, exists := excludeExt[ext]; exists { // skip this file ext
					return nil
				}
			}

			if len(name) != 0 {
				if _, exists := excludeFiles[name]; exists { // skip this file name
					return nil
				} else if params.ExcludeDotfiles && name[:1] == "." {
					return nil
				}
				if len(excludeFilesByRegex) != 0 {
					for _, regex := range excludeFilesByRegex {
						isExcludedFile, _ := regexp.Match(regex, []byte(name))
						if isExcludedFile {
							return nil
						}
					}
				}
			}

			// check parent dir
			dirName := filepath.Base(filepath.Dir(path))
			if len(dirName) != 0 {
				if err := skipDir(path, dirName); err != nil {
					if info.IsDir() {
						return err
					}
				}
			}
		}

		if err := addFileToTarByPath(tw, path, baseDir, info); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, fileName := range params.Files {
		addFile, err := os.Open(fileName)
		if err != nil {
			return nil, errors.Wrap(err, "error opening file to add to tar")
		}
		// Closed at the end of each iteration rather than deferred: a defer
		// inside a loop holds every handle open until the whole function
		// returns. Named addFile so it cannot be mistaken for the tar file
		// above, which it used to shadow.
		err = addFileToTar(tw, addFile, "")
		addFile.Close()
		if err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, errors.Wrap(err, "error closing tar file")
	}
	// Close the tar file itself, not just the writer wrapped around it, before
	// gzipping and removing it below. The deferred Close above does not fire
	// until this function returns, and Windows refuses to remove a file that
	// still has an open handle -- unlike Unix, where unlinking an open file is
	// fine. The deferred call stays for the early-return paths; closing twice
	// just yields os.ErrClosed, which a bare deferred call discards.
	if err := file.Close(); err != nil {
		return nil, errors.Wrap(err, "error closing tar file handle")
	}

	// gzip compress the tar file
	if err := packGzip(tarFileName, params.Output); err != nil {
		return nil, err
	}
	if err := os.Remove(tarFileName); err != nil {
		return nil, errors.Wrap(err, "error removing temp tar file")
	}

	destPathAbsolute, err := filepath.Abs(params.Output)
	if err != nil {
		return nil, errors.Wrap(err, "can not get absolute path of gzipped file")
	}
	return &BundleResponse{
		Output: destPathAbsolute,
	}, nil
}

// addFileToTarByPath adds the file under path (with FileInfo info) to the tar
// archive tw, keeping the directory structure relative to baseDir (baseDir
// becomes the new root in the archive).
func addFileToTarByPath(tw *tar.Writer, path string, baseDir string, info os.FileInfo) error {
	// write header; sub dirs are created by adding files with a path: mydir/subdir/file.txt
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return errors.Wrap(err, "can not read file header for tar")
	}
	// ToSlash, because tar names are always slash-separated whatever the host
	// separator is -- an archive written on Windows has to extract to the same
	// tree everywhere. Without it filepath.Join yields "docs\x.md" there, which
	// is not a path in the archive at all: it is one root-level file with a
	// backslash in its name. The v0.1.2 win64 release shipped exactly that.
	// On Unix ToSlash is a no-op, which is correct -- a backslash is a legal
	// character in a Unix filename and must be left alone.
	header.Name = filepath.ToSlash(filepath.Join(baseDir, strings.TrimPrefix(path, baseDir)))
	if err := tw.WriteHeader(header); err != nil {
		return errors.Wrap(err, "can not write tar header")
	}

	// copy file data
	curFile, err := os.Open(path)
	if err != nil {
		return errors.Wrap(err, "error opening file to add to tar")
	}
	defer curFile.Close()
	if _, err := io.Copy(tw, curFile); err != nil {
		return errors.Wrap(err, "error copying file to tar")
	}

	return nil
}

// addFileToTar adds the given file to the tar archive tw. The pathPrefix adds
// the file to a sub directory in the archive (e.g. "foo/baa"); leave it empty
// to add to the root.
func addFileToTar(tw *tar.Writer, file *os.File, pathPrefix string) error {
	info, err := os.Lstat(file.Name()) // don't follow symlinks, just like Walk()
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return errors.Wrap(err, "can not read file header for tar")
	}
	// Slash-separated for the same reason as in addFileToTarByPath. Latent while
	// every caller passes an empty pathPrefix, but it is the same mistake.
	header.Name = filepath.ToSlash(filepath.Join(pathPrefix, filepath.Base(file.Name())))
	if err := tw.WriteHeader(header); err != nil {
		return errors.Wrap(err, "can not write tar header")
	}

	if _, err := io.Copy(tw, file); err != nil {
		return errors.Wrap(err, "error copying file to tar")
	}

	return nil
}

func packGzip(source string, dest string) error {
	fileSrc, err := os.Open(source)
	if err != nil {
		return errors.Wrap(err, "can not read source to gzip")
	}
	defer fileSrc.Close()
	fileDest, err := os.Create(dest)
	if err != nil {
		return errors.Wrap(err, "can not create gzip file")
	}
	defer fileDest.Close()

	zw, err := gzip.NewWriterLevel(fileDest, gzip.DefaultCompression)
	if err != nil {
		return err
	}

	// Setting the Header fields is optional.
	zw.Name = filepath.Base(dest)
	zw.Comment = ""
	zw.ModTime = time.Now()

	if _, err := io.Copy(zw, fileSrc); err != nil {
		return errors.Wrap(err, "error copying data to gzip file")
	}
	if err := zw.Close(); err != nil {
		return errors.Wrap(err, "error closing gzip file")
	}

	return nil
}

// StringSet is a set of strings.
type StringSet map[string]struct{}

func createSetFromSlice(input []string) StringSet {
	set := make(StringSet, len(input))
	for _, key := range input {
		set[key] = struct{}{}
	}
	return set
}
