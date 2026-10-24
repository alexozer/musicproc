package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runCmd(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s <args>: %v:\n%s", cmd.Path, err, out)
	}
	return nil
}

type Tree struct {
	Base  string
	Files map[string]os.FileInfo
}

func getFSTree(dirPath string) (*Tree, error) {
	files := make(map[string]os.FileInfo)
	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			relPath, err := filepath.Rel(dirPath, path)
			if err != nil {
				return err
			}
			if files[relPath], err = d.Info(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error walking path '%s': %w", dirPath, err)
	}
	return &Tree{Base: dirPath, Files: files}, nil
}

func copyFile(src string, dest string, mtime time.Time) error {
	tempDest := fmt.Sprintf("%s.tmp", dest)

	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer sourceFile.Close()
	sourceReader := bufio.NewReaderSize(sourceFile, 256*1024)

	destFile, err := os.Create(tempDest)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destFile.Close()
	destWriter := bufio.NewWriter(destFile)

	_, err = io.Copy(destWriter, sourceReader)
	if err != nil {
		return fmt.Errorf("failed to copy data: %w", err)
	}

	// Move temp file into place
	if err := os.Chtimes(tempDest, time.Time{}, mtime); err != nil {
		return fmt.Errorf("failed to change mtime of '%s' to mtime of src: %w", tempDest, err)
	}
	if err := os.Rename(tempDest, dest); err != nil {
		return fmt.Errorf("failed to rename '%s' -> '%s': %w", tempDest, dest, err)
	}

	return nil
}

func hardlinkOrCopyFile(src string, dest string, mtime time.Time) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("failed to mkdir '%s': %w", filepath.Dir(dest), err)
	}
	// Fails if e.g. src and dest are on different filesystems
	if os.Link(src, dest) != nil {
		return copyFile(src, dest, mtime)
	}
	return nil
}
