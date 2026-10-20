package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"go.senan.xyz/taglib"
	"golang.org/x/sync/errgroup"
)

const (
	OPUS_BITRATE = "160k"
)

func runCmd(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s <args>: %v:\n%s", cmd.Path, err, out)
	}
	return nil
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

func transcodeAudioFileToOpus(srcPath string, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("Failed to make output dir: %w", err)
	}

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", srcPath,
		"-map", "0:a", "-vn",
		"-c:a", "libopus", "-b:a", OPUS_BITRATE, "-vbr", "on",
		"-f", "ogg", // Possibly using temp file extension, so name the muxer explicitly
		destPath,
	}
	cmd := exec.Command("ffmpeg", args...)
	if err := runCmd(cmd); err != nil {
		return fmt.Errorf("Failed to run ffmpeg: %w", err)
	}
	return nil
}

type Tree struct {
	Base  string
	Files map[string]os.FileInfo
}

func mapPath(srcPath string) string {
	lowerExt := strings.ToLower(filepath.Ext(srcPath))
	if lowerExt == ".flac" || lowerExt == ".wav" || lowerExt == ".aiff" {
		trimmed := strings.TrimSuffix(srcPath, filepath.Ext(srcPath))
		return fmt.Sprintf("%s.opus", trimmed)
	}
	return srcPath
}

func copyFile(src string, dest string) error {
	tempDest := fmt.Sprintf("%s.tmp", dest)
	if err := os.MkdirAll(filepath.Dir(tempDest), 0755); err != nil {
		return fmt.Errorf("failed to mkdir '%s': %w", filepath.Dir(tempDest), err)
	}

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
	if err := os.Rename(tempDest, dest); err != nil {
		return fmt.Errorf("failed to rename '%s' -> '%s': %w", tempDest, dest, err)
	}

	return nil
}

type FilePlan struct {
	Src  string
	Dest string
}

type Plan struct {
	DeleteFiles  []string
	CopyFiles    []FilePlan
	ConvertFiles []FilePlan
}

func planTranscode(srcTree *Tree, destTree *Tree) *Plan {
	var deleteFiles []string
	var copyFiles []FilePlan
	var convertFiles []FilePlan

	// Determine files to delete
	deadDestFiles := make(map[string]os.FileInfo)
	maps.Copy(deadDestFiles, destTree.Files)
	for srcFile := range srcTree.Files {
		destPath := mapPath(srcFile)
		delete(deadDestFiles, destPath)
	}
	for deadDestFile := range deadDestFiles {
		destFileAbs := filepath.Join(destTree.Base, deadDestFile)
		deleteFiles = append(deleteFiles, destFileAbs)
	}

	// Determine files to copy/convert
	for srcFile, srcInfo := range srcTree.Files {
		destFile := mapPath(srcFile)
		destInfo, destExists := destTree.Files[destFile]
		srcNewer := !destExists || srcInfo.ModTime().Compare(destInfo.ModTime()) == 1

		if srcNewer {
			srcAbs := filepath.Join(srcTree.Base, srcFile)
			destAbs := filepath.Join(destTree.Base, destFile)

			if destFile == srcFile {
				// Lossy format (or unsupported lossless), just copy
				copyFiles = append(copyFiles, FilePlan{srcAbs, destAbs})
			} else {
				// Lossless format - transcode to Opus
				convertFiles = append(convertFiles, FilePlan{srcAbs, destAbs})
			}
		}
	}

	sort.Strings(deleteFiles)
	sort.Slice(copyFiles, func(i, j int) bool {
		return copyFiles[i].Src < copyFiles[j].Src
	})
	sort.Slice(convertFiles, func(i, j int) bool {
		return convertFiles[i].Src < convertFiles[j].Src
	})

	return &Plan{DeleteFiles: deleteFiles, CopyFiles: copyFiles, ConvertFiles: convertFiles}
}

func transferTags(src string, dest string) error {
	tags, err := taglib.ReadTags(src)
	if err != nil {
		return fmt.Errorf("failed to read tags from '%s': %w", src, err)
	}

	// Some track numbers look like: "3/18" instead of "3"
	trackNumbers := tags["TRACKNUMBER"]
	for i, tn := range trackNumbers {
		left, _, _ := strings.Cut(tn, "/")
		trackNumbers[i] = left
	}

	if err := taglib.WriteTags(dest, tags, 0); err != nil {
		return fmt.Errorf("failed to write tags to '%s': %w", dest, err)
	}

	return nil
}

func convertFile(src string, dest string) error {
	log.Printf("Converting '%s' -> '%s'\n", src, dest)

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("failed to mkdir '%s': %w", dest, err)
	}

	tempDest := fmt.Sprintf("%s.tmp", dest)
	if err := transcodeAudioFileToOpus(src, tempDest); err != nil {
		return fmt.Errorf("failed to transcode '%s' -> '%s': %w", src, tempDest, err)
	}

	// Set cover image
	image, err := taglib.ReadImage(src)
	if err != nil {
		return fmt.Errorf("failed to read image from '%s': %w", src, err)
	}
	if err := taglib.WriteImage(tempDest, image); err != nil {
		return fmt.Errorf("failed to write image to '%s': %w", tempDest, err)
	}

	// Set tags
	if err := transferTags(src, tempDest); err != nil {
		return err
	}

	// Move temp file into place
	if err := os.Rename(tempDest, dest); err != nil {
		return fmt.Errorf("failed to rename '%s' -> '%s': %w", tempDest, dest, err)
	}

	return nil
}

func doStuff() error {
	if len(os.Args) < 3 {
		log.Fatal("Usage: <src dir> <dest dir>")
	}
	srcDir := os.Args[1]
	destDir := os.Args[2]

	srcTree, err := getFSTree(srcDir)
	if err != nil {
		return fmt.Errorf("failed to get src tree: %w", err)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create dest dir: %w", err)
	}
	destTree, err := getFSTree(destDir)
	if err != nil {
		return fmt.Errorf("failed to get dest tree: %w", err)
	}

	plan := planTranscode(srcTree, destTree)
	log.Printf("Plan: delete %d file(s), copy %d file(s), convert %d file(s)\n",
		len(plan.DeleteFiles), len(plan.CopyFiles), len(plan.ConvertFiles))

	if len(plan.DeleteFiles) > 0 {
		log.Printf("Deleting %d file(s)\n", len(plan.DeleteFiles))
		for _, delFile := range plan.DeleteFiles {
			if err := os.Remove(delFile); err != nil {
				return fmt.Errorf("failed to del file '%s': %w", delFile, err)
			}
		}
	}

	if len(plan.CopyFiles) > 0 {
		log.Printf("Copying %d file(s)\n", len(plan.CopyFiles))
		for _, f := range plan.CopyFiles {
			if err := copyFile(f.Src, f.Dest); err != nil {
				return fmt.Errorf("failed to copy file '%s' -> '%s': %w", f.Src, f.Dest, err)
			}
		}
	}

	errGroup, _ := errgroup.WithContext(context.Background())
	errGroup.SetLimit(runtime.NumCPU())
	for _, f := range plan.ConvertFiles {
		errGroup.Go(func() error {
			return convertFile(f.Src, f.Dest)
		})
	}
	if err := errGroup.Wait(); err != nil {
		return err
	}

	return nil
}

func main() {
	if err := doStuff(); err != nil {
		log.Fatal(err)
	}
}
