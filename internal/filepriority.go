package internal

import (
	"path/filepath"
	"regexp"
	"strings"
)

// File priority modes (settings / TORRENT_FILE_PRIORITY).
const (
	filePriorityAll         = "all"
	filePriorityEpisodes    = "episodes"     // prefer SxxEyy; skip samples/extras; skip season packs when episodes exist
	filePrioritySeasonPacks = "season_packs" // prefer season packs; skip single episodes when packs exist
)

var (
	reEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,2}e\d{1,3}|\d{1,2}x\d{1,3})(?:[^a-z0-9]|$)`)
	rePack    = regexp.MustCompile(`(?i)(?:season[ ._-]?\d{1,2}(?:[ ._-]?complete)?|complete[ ._-]?season|season[ ._-]?pack|(?:^|[^a-z0-9])s\d{1,2}(?:[^a-z0-9e]|$)|\.s\d{1,2}\.)`)
)

type fileClass int

const (
	classJunk fileClass = iota
	classEpisode
	classPack
	classOther
)

func normalizeFilePriorityMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", filePriorityAll, "download_all":
		return filePriorityAll
	case filePriorityEpisodes, "episode", "prefer_episodes", "single_episodes":
		return filePriorityEpisodes
	case filePrioritySeasonPacks, "packs", "prefer_packs", "season_pack":
		return filePrioritySeasonPacks
	default:
		return filePriorityAll
	}
}

func classifyFilePath(path string) fileClass {
	base := strings.ToLower(filepath.Base(path))
	full := strings.ToLower(filepath.ToSlash(path))

	if isJunkMediaPath(full, base) {
		return classJunk
	}
	// Episode patterns win over pack (S01E02 is not a pack).
	if reEpisode.MatchString(full) {
		return classEpisode
	}
	if rePack.MatchString(full) {
		return classPack
	}
	return classOther
}

func isJunkMediaPath(full, base string) bool {
	junkSubs := []string{
		"/sample/", "/samples/", "/extras/", "/featurettes/", "/screens/",
		"/proof/", "/subs/",
	}
	for _, s := range junkSubs {
		if strings.Contains(full, s) {
			return true
		}
	}
	if strings.Contains(base, "sample") || strings.Contains(base, "trailer") {
		return true
	}
	switch filepath.Ext(base) {
	case ".nfo", ".txt", ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".sfv", ".md5":
		return true
	}
	return false
}

// selectFilesToDownload returns whether each path should be downloaded.
// When mode is all or classification yields no preference, all non-empty paths are selected.
func selectFilesToDownload(paths []string, mode string) []bool {
	n := len(paths)
	out := make([]bool, n)
	if n == 0 {
		return out
	}
	mode = normalizeFilePriorityMode(mode)
	if mode == filePriorityAll || n == 1 {
		for i := range out {
			out[i] = true
		}
		return out
	}

	classes := make([]fileClass, n)
	var hasEp, hasPack, hasOther bool
	for i, p := range paths {
		c := classifyFilePath(p)
		classes[i] = c
		switch c {
		case classEpisode:
			hasEp = true
		case classPack:
			hasPack = true
		case classOther:
			hasOther = true
		}
	}

	switch mode {
	case filePriorityEpisodes:
		want := classEpisode
		if !hasEp {
			want = classPack
		}
		if hasEp || hasPack {
			for i, c := range classes {
				out[i] = c == want || c == classOther
			}
			return out
		}
	case filePrioritySeasonPacks:
		if hasPack {
			for i, c := range classes {
				out[i] = c == classPack // skip single episodes + junk when a pack is present
			}
			return out
		}
		if hasEp {
			for i, c := range classes {
				out[i] = c == classEpisode || c == classOther
			}
			return out
		}
	}

	// Fallback: everything except junk (or all if nothing classified).
	anyKeep := false
	for i, c := range classes {
		out[i] = c != classJunk
		if out[i] {
			anyKeep = true
		}
	}
	if !anyKeep {
		for i := range out {
			out[i] = true
		}
	}
	_ = hasOther
	return out
}
