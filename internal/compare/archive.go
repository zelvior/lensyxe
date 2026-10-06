// This file implements tar extraction for `lensyxe compare`.
//
// The standard library's archive/tar is used rather than an external `tar`
// binary so the behaviour is identical on every platform Lensyxe targets,
// including Windows where no tar ships by default.
//
// Extraction is hardened: entries whose destination escapes the target
// directory are rejected, which matters because a repository tarball is
// untrusted input as far as this code is concerned.
package compare

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveFileBytes caps a single extracted file, matching the code
// analyzer's default size cap so a repository containing a huge blob cannot
// exhaust the temp directory.
const maxArchiveFileBytes = 64 << 20

// extractTar unpacks archivePath into destDir.
func extractTar(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, hdr.Size); err != nil {
				return err
			}
		default:
			// Symlinks, devices, and hard links are skipped: the analyzer
			// ignores them anyway, and creating them would be a needless risk.
			continue
		}
	}
}

// writeFile creates one regular file from the archive stream.
//
// Git does not preserve permission bits in archive output by default, so files
// are created with the analyzer's expected readability.
func writeFile(target string, r io.Reader, size int64) error {
	if size > maxArchiveFileBytes {
		return fmt.Errorf("archive entry %s exceeds %d bytes", filepath.Base(target), int64(maxArchiveFileBytes))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
	}
	f, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	defer f.Close()

	// LimitReader guards against a header that lies about its size.
	if _, err := io.Copy(f, io.LimitReader(r, maxArchiveFileBytes)); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// safeJoin resolves name inside base, rejecting traversal attempts.
//
// archive entries use forward slashes regardless of platform, so paths are
// normalized with filepath.FromSlash before the containment check.
func safeJoin(base, name string) (string, error) {
	// Reject before cleaning, so a leading slash cannot be normalized away.
	// On Windows filepath.IsAbs("/x") is false, which would let the entry
	// through as a relative path.
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("archive entry %q has an absolute path", name)
	}

	cleaned := filepath.Clean(filepath.FromSlash(name))
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return base, nil
	}
	// Absolute paths and any parent traversal are refused outright.
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the extraction directory", name)
	}
	target := filepath.Join(base, cleaned)
	// Belt and braces: verify the resolved path is still inside base.
	rel, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("archive entry %q escapes the extraction directory", name)
	}
	return target, nil
}
