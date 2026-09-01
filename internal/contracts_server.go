package internal

import (
	"context"
	"fmt"
	"strings"

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
	s.m.mu.RLock()
	th, ok := s.m.torrents[req.GetTorrentId()]
	s.m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("torrent not found: %s", req.GetTorrentId())
	}
	return &cdlv1.GetTorrentResponse{Torrent: mapTorrentInfoFromHandle(th)}, nil
}

func (s *contractsServer) ListTorrents(ctx context.Context, req *cdlv1.ListTorrentsRequest) (*cdlv1.ListTorrentsResponse, error) {
	category := req.GetCategory()
	status := req.GetStatus()
	s.m.mu.RLock()
	out := make([]*cdlv1.TorrentInfo, 0, len(s.m.torrents))
	for _, th := range s.m.torrents {
		th.mu.RLock()
		label, st := th.Label, th.Status
		th.mu.RUnlock()
		if category != "" && !strings.EqualFold(label, category) {
			continue
		}
		if !torrentMatchesContractStatus(st, status) {
			continue
		}
		out = append(out, mapTorrentInfoFromHandle(th))
	}
	s.m.mu.RUnlock()
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
		SupportsFileSelection: true,
		SupportedProtocols:    []string{"torrent", "magnet"},
	}, nil
}

func torrentMatchesContractStatus(localStatus string, contractStatus cdlv1.TorrentStatus) bool {
	if contractStatus == cdlv1.TorrentStatus_TORRENT_STATUS_UNSPECIFIED {
		return true
	}
	switch contractStatus {
	case cdlv1.TorrentStatus_TORRENT_STATUS_DOWNLOADING:
		return localStatus == "downloading" || localStatus == "queued" || localStatus == "seeding"
	case cdlv1.TorrentStatus_TORRENT_STATUS_PAUSED:
		return localStatus == "paused"
	case cdlv1.TorrentStatus_TORRENT_STATUS_COMPLETED:
		return localStatus == "completed"
	case cdlv1.TorrentStatus_TORRENT_STATUS_FAILED:
		return localStatus == "error"
	default:
		return false
	}
}

func localStatusToContract(st string) cdlv1.TorrentStatus {
	switch st {
	case "downloading", "queued", "seeding":
		return cdlv1.TorrentStatus_TORRENT_STATUS_DOWNLOADING
	case "paused":
		return cdlv1.TorrentStatus_TORRENT_STATUS_PAUSED
	case "completed":
		return cdlv1.TorrentStatus_TORRENT_STATUS_COMPLETED
	case "error":
		return cdlv1.TorrentStatus_TORRENT_STATUS_FAILED
	default:
		return cdlv1.TorrentStatus_TORRENT_STATUS_UNKNOWN
	}
}

func mapTorrentInfoFromHandle(th *torrentHandle) *cdlv1.TorrentInfo {
	if th == nil {
		return nil
	}
	th.mu.RLock()
	defer th.mu.RUnlock()

	progress := 0.0
	if th.TotalSize > 0 {
		progress = float64(th.Downloaded) / float64(th.TotalSize) * 100
	}

	info := &cdlv1.TorrentInfo{
		Id:            th.ID,
		Name:          th.Name,
		InfoHash:      th.InfoHash,
		Size:          th.TotalSize,
		Downloaded:    th.Downloaded,
		Uploaded:      th.Uploaded,
		Progress:      progress,
		DownloadSpeed: int64(th.DownloadRate),
		UploadSpeed:   int64(th.UploadRate),
		Seeders:       th.Seeders,
		Leechers:      th.Peers,
		Status:        localStatusToContract(th.Status),
		SavePath:      th.SavePath,
		Category:      th.Label,
		Error:         th.ErrorStr,
	}
	if !th.AddedAt.IsZero() {
		info.AddedAt = timestamppb.New(th.AddedAt)
	}
	if th.CompletedAt != nil {
		info.CompletedAt = timestamppb.New(*th.CompletedAt)
	}
	for _, f := range th.Files {
		info.Files = append(info.Files, &cdlv1.TorrentFile{
			Path:       f.Path,
			Size:       f.Size,
			Downloaded: f.Downloaded,
			Wanted:     f.Wanted,
		})
	}
	return info
}
