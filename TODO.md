# TODO

- [x] Systemd service
- [x] Poll src directory for changes, for now
- [x] Copy files to .tmp first
- [X] Fix Adventure track numbers
- [x] Point navidrome / jellyfin at transcoded music library
- [x] Test on phone
- [x] Consider removing navidrome
- [ ] Race condition: may convert partially transferred files (like if file is transferred by Samba)
- [ ] Log messages should have appropriate systemd info/warn/error tag if possible

## Low priority

- [ ] Fix weird Unicode issue - some files are always transcoded
- [ ] Inotify watcher, based on what syncthing does?
- [ ] Delete empty directories
- [ ] Buffered / parallel file copying: why is it so slow?
