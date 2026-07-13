package core

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
)

// optimizeRunning guards against two overlapping optimize runs.
var optimizeRunning atomic.Bool

// OptimizeStreaming scans every .mp4 in the vault and losslessly remuxes files
// whose moov atom sits after the media data ("moov at end"). Those files force
// phones to fetch the far end of the file before playback can start, which
// reads as "video doesn't work" on mobile. The remux is a stream copy
// (-c copy -movflags +faststart): no re-encode, no quality loss, takes seconds
// per file. Runs in the background and emits "optimize" progress events.
func (c *Core) OptimizeStreaming() {
	if !optimizeRunning.CompareAndSwap(false, true) {
		return // already running
	}
	go func() {
		defer optimizeRunning.Store(false)

		var todo []string
		_ = filepath.WalkDir(c.mediaRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == stateDirName {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".mp4") && needsFaststart(path) {
				todo = append(todo, path)
			}
			return nil
		})

		total := len(todo)
		fixed, failed := 0, 0
		c.emit("optimize", map[string]any{"done": 0, "total": total, "fixed": 0, "failed": 0})
		for i, path := range todo {
			if remuxFaststart(path) {
				fixed++
			} else {
				failed++
			}
			c.emit("optimize", map[string]any{
				"done": i + 1, "total": total, "fixed": fixed, "failed": failed,
				"name": filepath.Base(path),
			})
		}
		c.emit("optimize", map[string]any{
			"done": total, "total": total, "fixed": fixed, "failed": failed, "finished": true,
		})
	}()
}

// needsFaststart reports whether the mp4 stores its moov atom after the mdat
// atom. It walks the top-level box headers (8–16 bytes each), so the whole
// check reads only a handful of bytes no matter the file size.
func needsFaststart(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var off int64
	hdr := make([]byte, 16)
	for {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return false // hit EOF without seeing moov or mdat
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		box := string(hdr[4:8])
		switch box {
		case "moov":
			return false // header first: already streams instantly
		case "mdat":
			return true // media before index: phones stall on this
		}
		switch size {
		case 1: // 64-bit largesize follows the header
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return false
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
			if size < 16 {
				return false
			}
		case 0:
			return false // box extends to EOF and it's neither moov nor mdat
		default:
			if size < 8 {
				return false // corrupt header; leave the file alone
			}
		}
		off += size
	}
}

// remuxFaststart rewrites the file with the moov atom up front via a lossless
// stream copy, replacing the original atomically and preserving its mod time.
func remuxFaststart(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + ".faststart.tmp.mp4"
	cmd := exec.Command(ffmpegPath(), "-y", "-v", "error", "-i", path,
		"-map", "0", "-c", "copy", "-movflags", "+faststart", tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	// Sanity: the remux must be roughly the same size (a truncated output would
	// mean ffmpeg bailed part-way; never replace the original with that).
	if ts, err := os.Stat(tmp); err != nil || ts.Size() < st.Size()*9/10 {
		_ = os.Remove(tmp)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	_ = os.Chtimes(path, st.ModTime(), st.ModTime())
	return true
}

func ffmpegPath() string {
	if dir := ffmpegDir(); dir != "" {
		for _, name := range []string{"ffmpeg.exe", "ffmpeg"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return "ffmpeg"
}
