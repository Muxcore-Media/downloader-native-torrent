package internal

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type pieceLayout struct {
	PieceLength int64
	PieceHashes [][]byte
	Files       []fileInfo
	TotalLength int64
}

type diskFile struct {
	Abs  string
	Rel  string
	Base string
	Size int64
}

func isPartialsSavePath(savePath string) bool {
	parent := filepath.Dir(savePath)
	return filepath.Base(filepath.Dir(parent)) == "partials"
}

func layoutFingerprint(files []fileInfo) string {
	sizes := make([]string, 0, len(files))
	var total int64
	for _, f := range files {
		if f.Size > 0 {
			sizes = append(sizes, strconv.FormatInt(f.Size, 10))
			total += f.Size
		}
	}
	if len(sizes) == 0 {
		return ""
	}
	sort.Strings(sizes)
	return strconv.FormatInt(total, 10) + ":" + strings.Join(sizes, ":")
}

func diskFingerprint(files []diskFile) string {
	sizes := make([]string, 0, len(files))
	var total int64
	for _, f := range files {
		if f.Size > 0 {
			sizes = append(sizes, strconv.FormatInt(f.Size, 10))
			total += f.Size
		}
	}
	if len(sizes) == 0 {
		return ""
	}
	sort.Strings(sizes)
	return strconv.FormatInt(total, 10) + ":" + strings.Join(sizes, ":")
}

func listDiskFiles(root string) ([]diskFile, error) {
	var out []diskFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out = append(out, diskFile{
			Abs:  path,
			Rel:  filepath.ToSlash(rel),
			Base: filepath.Base(path),
			Size: info.Size(),
		})
		return nil
	})
	return out, err
}

func mapLayoutToDisk(layout []fileInfo, disk []diskFile) []diskFile {
	used := make([]bool, len(disk))
	mapped := make([]diskFile, len(layout))
	for i, lf := range layout {
		wantBase := filepath.Base(strings.ReplaceAll(lf.Path, "\\", "/"))
		found := -1
		for j, d := range disk {
			if used[j] {
				continue
			}
			if d.Rel == lf.Path || d.Base == wantBase {
				found = j
				break
			}
		}
		if found < 0 {
			for j, d := range disk {
				if used[j] {
					continue
				}
				if d.Size == lf.Size {
					count := 0
					for k, o := range disk {
						if !used[k] && o.Size == lf.Size {
							count++
						}
					}
					if count == 1 {
						found = j
						break
					}
				}
			}
		}
		if found < 0 {
			return nil
		}
		used[found] = true
		mapped[i] = disk[found]
	}
	return mapped
}

func readMappedRange(mapped []diskFile, layout []fileInfo, offset, length int64) ([]byte, bool) {
	if length <= 0 || len(mapped) != len(layout) {
		return nil, false
	}
	buf := make([]byte, 0, length)
	var pos int64
	remain := length
	for i, lf := range layout {
		end := pos + lf.Size
		if offset >= end {
			pos = end
			continue
		}
		if remain <= 0 {
			break
		}
		startInFile := offset - pos
		if startInFile < 0 {
			startInFile = 0
		}
		avail := mapped[i].Size - startInFile
		if avail <= 0 {
			return nil, false
		}
		need := remain
		if avail < need {
			need = avail
		}
		if startInFile+need > lf.Size {
			return nil, false
		}
		f, err := os.Open(mapped[i].Abs)
		if err != nil {
			return nil, false
		}
		chunk := make([]byte, need)
		n, err := f.ReadAt(chunk, startInFile)
		_ = f.Close()
		if n != int(need) || (err != nil && err != io.EOF) {
			return nil, false
		}
		buf = append(buf, chunk...)
		remain -= need
		offset += need
		pos = end
	}
	if remain > 0 {
		return nil, false
	}
	return buf, true
}

// tryLinkSiblingPartials hardlinks files from a sibling partial dir when at least one
// full piece verifies. mismatch leaves the sibling untouched. inconclusive is a no-op.
func tryLinkSiblingPartials(savePath string, layout pieceLayout) (bool, error) {
	if !isPartialsSavePath(savePath) || layout.PieceLength <= 0 || len(layout.PieceHashes) == 0 || layout.TotalLength <= 0 {
		return false, nil
	}
	parent := filepath.Dir(savePath)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return false, nil
	}
	self := filepath.Clean(savePath)
	wantFP := layoutFingerprint(layout.Files)
	if wantFP == "" {
		return false, nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sib := filepath.Join(parent, e.Name())
		if filepath.Clean(sib) == self {
			continue
		}
		disk, err := listDiskFiles(sib)
		if err != nil || len(disk) == 0 {
			continue
		}
		if diskFingerprint(disk) != wantFP {
			continue
		}
		mapped := mapLayoutToDisk(layout.Files, disk)
		if mapped == nil {
			continue
		}
		verdict := verifyMappedPieces(mapped, layout)
		if verdict != pieceMatch {
			continue
		}
		if err := linkMappedFiles(mapped, layout.Files, savePath); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

const (
	pieceUnknown = iota
	pieceMatch
	pieceMismatch
)

func verifyMappedPieces(mapped []diskFile, layout pieceLayout) int {
	sawFull := false
	for i, h := range layout.PieceHashes {
		off := int64(i) * layout.PieceLength
		plen := layout.PieceLength
		if off+plen > layout.TotalLength {
			plen = layout.TotalLength - off
		}
		if plen <= 0 {
			continue
		}
		data, ok := readMappedRange(mapped, layout.Files, off, plen)
		if !ok {
			continue
		}
		sawFull = true
		sum := sha1.Sum(data)
		if hex.EncodeToString(sum[:]) == hex.EncodeToString(h) {
			return pieceMatch
		}
		return pieceMismatch
	}
	if !sawFull {
		return pieceUnknown
	}
	return pieceMismatch
}

func linkMappedFiles(mapped []diskFile, layout []fileInfo, destRoot string) error {
	for i, lf := range layout {
		dst := lf.Path
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(destRoot, filepath.FromSlash(lf.Path))
		}
		if err := linkOrCopy(mapped[i].Abs, dst); err != nil {
			return err
		}
	}
	return nil
}

func linkOrCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func resolveSavePath(dataDir, savePath string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir != "" {
		dataDir = filepath.Clean(dataDir)
	}
	savePath = strings.TrimSpace(savePath)
	if savePath == "" {
		return dataDir
	}
	if filepath.IsAbs(savePath) {
		return filepath.Clean(savePath)
	}
	if dataDir == "" {
		return filepath.Clean(savePath)
	}
	abs := filepath.Clean(filepath.Join(dataDir, savePath))
	if !pathInsideRoot(dataDir, abs) {
		return dataDir
	}
	return abs
}

func joinSaveAndRelPath(savePath, file string) string {
	savePath = filepath.Clean(strings.TrimSpace(savePath))
	file = strings.TrimSpace(file)
	if file == "" {
		if savePath == "." {
			return ""
		}
		return savePath
	}
	file = filepath.Clean(file)
	if filepath.IsAbs(file) {
		return file
	}
	if savePath == "" || savePath == "." {
		return file
	}
	saveSlash := filepath.ToSlash(savePath)
	fileSlash := filepath.ToSlash(file)
	if fileSlash == saveSlash || strings.HasPrefix(fileSlash, saveSlash+"/") {
		return file
	}
	parts := strings.Split(saveSlash, "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		suffix := strings.Join(parts[i:], "/")
		if fileSlash != suffix && !strings.HasPrefix(fileSlash, suffix+"/") {
			continue
		}
		prefix := strings.Join(parts[:i], "/")
		if prefix == "" {
			if filepath.IsAbs(savePath) {
				return filepath.Clean(filepath.Join(string(filepath.Separator), file))
			}
			return file
		}
		return filepath.Clean(filepath.Join(filepath.FromSlash(prefix), file))
	}
	return filepath.Clean(filepath.Join(savePath, file))
}

func pathInsideRoot(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
