package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.senan.xyz/taglib"
	"golang.org/x/sync/errgroup"
	"golang.org/x/text/unicode/norm"
)

const (
	OPUS_BITRATE = "160k"
)

func transcodeAudioFileToOpus(srcPath string, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to make output dir: %w", err)
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
		return fmt.Errorf("failed to run ffmpeg: %w", err)
	}
	return nil
}

func shouldConvertFileType(path string) bool {
	lowerExt := strings.ToLower(filepath.Ext(path))
	return lowerExt == ".flac" || lowerExt == ".wav" || lowerExt == ".aiff"
}

func mapPath(srcPath string) string {
	if shouldConvertFileType(srcPath) {
		srcPath = strings.TrimSuffix(srcPath, filepath.Ext(srcPath)) + ".opus"
	}
	return norm.NFC.String(srcPath)
}

type FilePlan struct {
	Src  string
	Dest string
	// Copy src mtime to dest to robustly detect changes to src file in the face of race conditions
	ModTime time.Time
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
		// We copy src modtime to dest modtime during conversion, so checking for modtime equality
		// makes it more robust in the face of race conditions
		needsUpdate := !destExists || !srcInfo.ModTime().Equal(destInfo.ModTime())

		if needsUpdate {
			srcAbs := filepath.Join(srcTree.Base, srcFile)
			destAbs := filepath.Join(destTree.Base, destFile)
			plan := FilePlan{
				Src:     srcAbs,
				Dest:    destAbs,
				ModTime: srcInfo.ModTime(),
			}

			if shouldConvertFileType(srcFile) {
				// Lossless format - transcode to Opus
				convertFiles = append(convertFiles, plan)
			} else {
				// Lossy format, unsupported lossless, or non-audio file: just copy
				copyFiles = append(copyFiles, plan)
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

func convertFile(src string, dest string, mtime time.Time) error {
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
	if err := os.Chtimes(tempDest, time.Time{}, mtime); err != nil {
		return fmt.Errorf("failed to change mtime of '%s' to mtime of src: %w", tempDest, err)
	}
	if err := os.Rename(tempDest, dest); err != nil {
		return fmt.Errorf("failed to rename '%s' -> '%s': %w", tempDest, dest, err)
	}

	return nil
}

func executePlan(plan *Plan) error {
	log.Printf("Plan: delete %d file(s), hardlink/copy %d file(s), convert %d file(s)\n",
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
		log.Printf("Hardlinking/copying %d file(s)\n", len(plan.CopyFiles))
		for _, f := range plan.CopyFiles {
			if err := hardlinkOrCopyFile(f.Src, f.Dest, f.ModTime); err != nil {
				return fmt.Errorf("failed to hardlink/copy file '%s' -> '%s': %w", f.Src, f.Dest, err)
			}
		}
	}

	errGroup, _ := errgroup.WithContext(context.Background())
	errGroup.SetLimit(runtime.NumCPU())
	for _, f := range plan.ConvertFiles {
		errGroup.Go(func() error {
			return convertFile(f.Src, f.Dest, f.ModTime)
		})
	}
	if err := errGroup.Wait(); err != nil {
		return err
	}

	log.Printf("Finished executing plan\n")
	return nil
}

func iterate() error {
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
	if len(plan.DeleteFiles) > 0 || len(plan.CopyFiles) > 0 || len(plan.ConvertFiles) > 0 {
		executePlan(plan)
	}

	return nil
}

func main() {
	for {
		if err := iterate(); err != nil {
			log.Printf("%v\n", err)
		}
		time.Sleep(time.Second * 5)
	}
}
