// Package fileops is the file manager + backup helper layer, replacing
// fileops.py. safe_join (Join here) is the single chokepoint that prevents
// path traversal -- nothing in this package should touch the filesystem with
// a path that didn't come out of it.
package fileops

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrPathSecurity = errors.New("path escapes server data directory")

type Entry struct {
	Name  string
	IsDir bool
	Size  int64 // -1 for directories
	MTime time.Time
}

type Backup struct {
	Filename string
	SizeMB   float64
	MTime    time.Time
}

// Join resolves rel under root and guarantees the result stays within root.
func Join(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		// root may not exist yet (e.g. mkdir on a fresh subtree); fall back to the
		// non-symlink-resolved absolute path rather than failing outright.
		absRoot, _ = filepath.Abs(root)
	}
	rel = strings.TrimLeft(rel, "/")
	candidate := filepath.Join(absRoot, rel)
	candidate = filepath.Clean(candidate)

	rootWithSep := absRoot
	if !strings.HasSuffix(rootWithSep, string(filepath.Separator)) {
		rootWithSep += string(filepath.Separator)
	}
	if candidate != absRoot && !strings.HasPrefix(candidate, rootWithSep) {
		return "", ErrPathSecurity
	}
	return candidate, nil
}

func ListDir(root, rel string) ([]Entry, error) {
	target, err := Join(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil, os.ErrNotExist
	}
	dirEntries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		size := int64(-1)
		if !de.IsDir() {
			size = fi.Size()
		}
		entries = append(entries, Entry{
			Name:  de.Name(),
			IsDir: de.IsDir(),
			Size:  size,
			MTime: fi.ModTime(),
		})
	}
	// Directories first, then files, both alpha (matches Python: key=(is_file, name.lower())).
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // dirs before files
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func ReadTextFile(root, rel string, maxBytes int64) (string, error) {
	target, err := Join(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return "", os.ErrNotExist
	}
	if info.Size() > maxBytes {
		return "", fmt.Errorf("file too large to edit in the browser")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func WriteTextFile(root, rel, content string) error {
	target, err := Join(root, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

var secureFilenameStrip = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// SecureFilename mirrors werkzeug.utils.secure_filename closely enough for
// our purposes: strip directory components, collapse anything unsafe.
func SecureFilename(name string) string {
	name = filepath.Base(name)
	name = secureFilenameStrip.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	return name
}

func SaveUpload(root, relDir string, fileHeader *multipart.FileHeader) (string, error) {
	filename := SecureFilename(fileHeader.Filename)
	if filename == "" {
		return "", fmt.Errorf("invalid filename")
	}
	targetDir, err := Join(root, relDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	target, err := Join(root, filepath.Join(relDir, filename))
	if err != nil {
		return "", err
	}
	src, err := fileHeader.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return target, nil
}

func DeletePath(root, rel string) error {
	target, err := Join(root, rel)
	if err != nil {
		return err
	}
	absRoot, _ := filepath.Abs(root)
	if target == absRoot {
		return fmt.Errorf("%w: refusing to delete the server's root data directory", ErrPathSecurity)
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(target)
	}
	return os.Remove(target)
}

func MakeDir(root, rel string) error {
	target, err := Join(root, rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

func ResolveDownload(root, rel string) (string, error) {
	target, err := Join(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return "", os.ErrNotExist
	}
	return target, nil
}

// --- Backups -----------------------------------------------------------

func CreateBackup(backupsRoot, serverSlug, dataDir string) (string, error) {
	destDir := filepath.Join(backupsRoot, serverSlug)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	timestamp := time.Now().UTC().Format("20060102-150405")
	archivePath := filepath.Join(destDir, fmt.Sprintf("%s-%s.tar.gz", serverSlug, timestamp))

	f, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	err = filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// arcname="." in the Python version means entries are stored relative to
		// the data dir root (no leading server-slug/ component) -- match that.
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			defer src.Close()
			if _, err := io.Copy(tw, src); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return archivePath, nil
}

func ListBackups(backupsRoot, serverSlug string) ([]Backup, error) {
	destDir := filepath.Join(backupsRoot, serverSlug)
	entries, err := os.ReadDir(destDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var backups []Backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, Backup{
			Filename: e.Name(),
			SizeMB:   float64(info.Size()) / (1024 * 1024),
			MTime:    info.ModTime(),
		})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Filename > backups[j].Filename })
	return backups, nil
}

func ResolveBackup(backupsRoot, serverSlug, filename string) (string, error) {
	filename = SecureFilename(filename)
	target, err := Join(filepath.Join(backupsRoot, serverSlug), filename)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return "", os.ErrNotExist
	}
	return target, nil
}

func DeleteBackup(backupsRoot, serverSlug, filename string) error {
	target, err := ResolveBackup(backupsRoot, serverSlug, filename)
	if err != nil {
		return err
	}
	return os.Remove(target)
}
