package distribution

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var archiveTimestamp = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// Entry describes one file placed in a release archive.
type Entry struct {
	Source string
	Name   string
	Mode   fs.FileMode
}

func WriteArchive(target Target, output string, entries []Entry) error {
	normalized, err := normalizeEntries(entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(output), ".codexlink-package-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if target.OS == "windows" {
		err = writeZip(temp, normalized)
	} else {
		err = writeTarGz(temp, normalized)
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tempName, output)
}

func normalizeEntries(entries []Entry) ([]Entry, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("release archive has no files")
	}
	result := append([]Entry(nil), entries...)
	seen := make(map[string]struct{}, len(result))
	for i := range result {
		cleaned := path.Clean(strings.ReplaceAll(result[i].Name, "\\", "/"))
		if cleaned == "." || cleaned == "" || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("unsafe archive path %q", result[i].Name)
		}
		if _, exists := seen[cleaned]; exists {
			return nil, fmt.Errorf("duplicate archive path %q", cleaned)
		}
		if info, err := os.Stat(result[i].Source); err != nil {
			return nil, fmt.Errorf("stat %s: %w", result[i].Source, err)
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("release source is not a regular file: %s", result[i].Source)
		}
		seen[cleaned] = struct{}{}
		result[i].Name = cleaned
		if result[i].Mode == 0 {
			result[i].Mode = 0o644
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func writeTarGz(output io.Writer, entries []Entry) error {
	gz := gzip.NewWriter(output)
	gz.Header.ModTime = archiveTimestamp
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		info, err := os.Stat(entry.Source)
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name:    entry.Name,
			Mode:    int64(entry.Mode.Perm()),
			Size:    info.Size(),
			ModTime: archiveTimestamp,
			Uid:     0,
			Gid:     0,
			Uname:   "",
			Gname:   "",
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if err := copySource(tw, entry.Source); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(output io.Writer, entries []Entry) error {
	zw := zip.NewWriter(output)
	for _, entry := range entries {
		info, err := os.Stat(entry.Source)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(entry.Mode)
		header.SetModTime(archiveTimestamp)
		header.UncompressedSize64 = uint64(info.Size())
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if err := copySource(writer, entry.Source); err != nil {
			return err
		}
	}
	return zw.Close()
}

func copySource(destination io.Writer, source string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(destination, file)
	return err
}

func SHA256(file string) (string, error) {
	input, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer input.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func WriteSHA256(file string) (string, error) {
	digest, err := SHA256(file)
	if err != nil {
		return "", err
	}
	checksum := file + ".sha256"
	body := fmt.Sprintf("%s  %s\n", digest, filepath.Base(file))
	if err := os.WriteFile(checksum, []byte(body), 0o644); err != nil {
		return "", err
	}
	return checksum, nil
}
