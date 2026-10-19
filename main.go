package main

import (
	// "go.senan.xyz/taglib"
	"fmt"
	"log"
	"os/exec"
)

const (
	OPUS_BITRATE = "160k"
	TEST_INPUT   = "/Users/alex/Downloads/Nocturne (2026)/01 2515.flac"
	TEST_OUTPUT  = "/Users/alex/Downloads/01 2515.opus"
)

func runCmd(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s <args>: %v:\n%s", cmd.Path, err, out)
	}
	return nil
}

func transcodeAudioFileToOpus(srcPath string, destPath string) error {
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

func doStuff() error {
	return transcodeAudioFileToOpus(TEST_INPUT, TEST_OUTPUT)
}

func main() {
	if err := doStuff(); err != nil {
		log.Fatal(err)
	}
	// taglib.WriteImage()
	// taglib.WriteTags()
}
