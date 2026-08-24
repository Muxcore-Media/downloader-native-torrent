package internal

import (
	"context"
	"fmt"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// contractsServer adapts Module to the shared contracts-downloader DownloaderService.
type contractsServer struct {
	cdlv1.UnimplementedDownloaderServiceServer
	m *Module
}

func (s *contractsServer) AddTorrent(ctx context.Context, req *cdlv1.AddTorrentRequest) (*cdlv1.AddTorrentResponse, error) {
	resp, err := s.m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri:      req.GetTorrentUrl(),
		SavePath: req.GetSavePath(),
		Label:    req.GetCategory(),
		Paused:   req.GetPaused(),
	})
	if err != nil {
		return nil, err
	}
	return &cdlv1.AddTorrentResponse{
		TorrentId: resp.GetId(),
		Name:      resp.GetName(),
		InfoHash:  resp.GetInfoHash(),
	}, nil
}

func (s *contractsServer) RemoveTorrent(ctx context.Context, req *cdlv1.RemoveTorrentRequest) (*cdlv1.RemoveTorrentResponse, error) {
	_, err := s.m.RemoveTorrent(ctx, &downloaderv1.RemoveTorrentRequest{
		Id:          req.GetTorrentId(),
		DeleteFiles: req.GetDeleteFiles(),
	})
	if err != nil {
		return nil, err
	}
	return &cdlv1.RemoveTorrentResponse{Success: true}, nil
}

func (s *contractsServer) GetTorrent(ctx context.Context, req *cdlv1.GetTorrentRequest) (*cdlv1.GetTorrentResponse, error) {
	resp, err := s.m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: req.GetTorrentId()})
	if err != nil {
		return nil, err
	}
	return &cdlv1.GetTorrentResponse{Torrent: mapTorrentInfo(resp.GetTorrent())}, nil
}

func (s *contractsServer) ListTorrents(ctx context.Context, req *cdlv1.ListTorrentsRequest) (*cdlv1.ListTorrentsResponse, error) {
	filter := req.GetCategory()
	if filter == "" {
		filter = req.GetStatus()
	}
	resp, err := s.m.ListTorrents(ctx, &downloaderv1.ListTorrentsRequest{Filter: filter})
	if err != nil {
		return nil, err
	}
	out := make([]*cdlv1.TorrentInfo, 0, len(resp.GetTorrents()))
	for _, t := range resp.GetTorrents() {
		out = append(out, mapTorrentInfo(t))
	}
	return &cdlv1.ListTorrentsResponse{Torrents: out}, nil
}

func (s *contractsServer) PauseTorrent(ctx context.Context, req *cdlv1.PauseTorrentRequest) (*cdlv1.PauseTorrentResponse, error) {
	ok, err := s.m.pauseTorrent(req.GetTorrentId())
	if err != nil {
		return nil, err
	}
	return &cdlv1.PauseTorrentResponse{Success: ok}, nil
}

func (s *contractsServer) ResumeTorrent(ctx context.Context, req *cdlv1.ResumeTorrentRequest) (*cdlv1.ResumeTorrentResponse, error) {
	ok, err := s.m.resumeTorrent(req.GetTorrentId())
	if err != nil {
		return nil, err
	}
	return &cdlv1.ResumeTorrentResponse{Success: ok}, nil
}

func (s *contractsServer) GetCapabilities(context.Context, *cdlv1.GetCapabilitiesRequest) (*cdlv1.GetCapabilitiesResponse, error) {
	return &cdlv1.GetCapabilitiesResponse{
		SupportsCategories:    true,
		SupportsPausing:       true,
		SupportsFileSelection: false,
		SupportedProtocols:    []string{"torrent", "magnet"},
	}, nil
}

func (m *Module) pauseTorrent(id string) (bool, error) {
	m.mu.RLock()
	th, ok := m.torrents[id]
	m.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("torrent %q not found", id)
	}
	th.mu.Lock()
	if th.Status == "completed" || th.Status == "failed" || th.Status == "removed" {
		th.mu.Unlock()
		return false, fmt.Errorf("cannot pause torrent in status %s", th.Status)
	}
	if th.session != nil {
		th.session.PauseDownload()
	}
	th.Status = "paused"
	th.mu.Unlock()
	m.persistActiveTorrents()
	return true, nil
}

func (m *Module) resumeTorrent(id string) (bool, error) {
	m.mu.RLock()
	th, ok := m.torrents[id]
	m.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("torrent %q not found", id)
	}
	th.mu.Lock()
	if th.Status != "paused" {
		th.mu.Unlock()
		return false, fmt.Errorf("torrent is not paused")
	}
	session := th.session
	th.Status = "downloading"
	resumeCh := th.resumeCh
	th.mu.Unlock()

	select {
	case resumeCh <- struct{}{}:
	default:
	}
	if session != nil {
		m.beginDownload(session)
	}
	m.persistActiveTorrents()
	return true, nil
}

func mapTorrentInfo(t *downloaderv1.TorrentInfo) *cdlv1.TorrentInfo {
	if t == nil {
		return nil
	}
	info := &cdlv1.TorrentInfo{
		Id:            t.GetId(),
		Name:          t.GetName(),
		InfoHash:      t.GetInfoHash(),
		Size:          t.GetTotalSize(),
		Downloaded:    t.GetDownloaded(),
		Progress:      t.GetProgress(),
		DownloadSpeed: int64(t.GetDownloadRate()),
		UploadSpeed:   int64(t.GetUploadRate()),
		Seeders:       t.GetSeeders(),
		Leechers:      t.GetPeers(),
		Status:        t.GetStatus(),
		SavePath:      t.GetSavePath(),
		Category:      t.GetLabel(),
	}
	if t.GetAddedAt() != "" {
		if ts, err := time.Parse(time.RFC3339, t.GetAddedAt()); err == nil {
			info.AddedAt = timestamppb.New(ts)
		}
	}
	if t.GetCompletedAt() != "" {
		if ts, err := time.Parse(time.RFC3339, t.GetCompletedAt()); err == nil {
			info.CompletedAt = timestamppb.New(ts)
		}
	}
	for _, f := range t.GetFiles() {
		info.Files = append(info.Files, &cdlv1.TorrentFile{
			Path:       f.GetPath(),
			Size:       f.GetSize(),
			Downloaded: f.GetDownloaded(),
			Wanted:     true,
		})
	}
	return info
}
