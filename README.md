# `musicproc`

Incrementally transcodes a music library from lossless formats (FLAC/WAV/etc.) to Opus.
Other files (lossy audio, album covers, lyrics files, etc.) are copied over unchanged.

I use this to prepare a music library to sync to my phone.

## What `musicproc` handles for you

- Mirrors a given read-only source directory to a destination directory with transcoded files
- Transcodes lossless audio files (.flac, .wav, etc.) to very high-quality Opus
- Copies source media tags (album art, artist/track info, etc.) to Opus files
- Performs unicode normalization on destination file paths to prevent renaming by e.g. Syncthing
- Robust incremental conversion by matching filepaths + exact modtime
