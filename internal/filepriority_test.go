package internal

import "testing"

func TestSelectFilesEpisodesPrefersEpisodeOverPack(t *testing.T) {
	paths := []string{
		"Show.S01E01.mkv",
		"Show.Season.1.Complete.mkv",
		"Sample/sample.mkv",
		"Show.S01E01.nfo",
	}
	got := selectFilesToDownload(paths, filePriorityEpisodes)
	want := []bool{true, false, false, false}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("i=%d got=%v want=%v (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestSelectFilesSeasonPacksPrefersPack(t *testing.T) {
	paths := []string{
		"Show.S01E01.mkv",
		"Show.S01.Complete.mkv",
		"extras/featurette.mkv",
	}
	got := selectFilesToDownload(paths, filePrioritySeasonPacks)
	if !got[1] || got[0] || got[2] {
		t.Fatalf("got=%v", got)
	}
}

func TestSelectFilesAll(t *testing.T) {
	paths := []string{"a.mkv", "sample/x.mkv"}
	got := selectFilesToDownload(paths, filePriorityAll)
	if !got[0] || !got[1] {
		t.Fatalf("all should keep everything: %v", got)
	}
}

func TestClassifyFilePath(t *testing.T) {
	if classifyFilePath("Foo.S02E03.1080p.mkv") != classEpisode {
		t.Fatal("episode")
	}
	if classifyFilePath("Foo.Season.2.Complete.mkv") != classPack {
		t.Fatal("pack")
	}
	if classifyFilePath("Foo/sample/foo-sample.mkv") != classJunk {
		t.Fatal("junk")
	}
}

func TestNormalizeFilePriorityMode(t *testing.T) {
	if normalizeFilePriorityMode("prefer_episodes") != filePriorityEpisodes {
		t.Fatal()
	}
	if normalizeFilePriorityMode("weird") != filePriorityAll {
		t.Fatal()
	}
}
