package internal

import (
	"bytes"
	"io"
	"os"
	"strings"
)

func fixtureMediaSeed() string {
	if v := strings.TrimSpace(os.Getenv("QBIT_FIXTURE_MEDIA_SEED")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("FIXTURE_MEDIA_SEED"))
}

func copyFixtureSeed(seed, dest string) error {
	src, err := os.Open(seed)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Close()
}

const (
	// fixtureHeaderSize is the real (non-sparse) prefix of the fixture payload:
	// the same i%251 byte pattern the engine has always written.
	fixtureHeaderSize int64 = 8 * 1024

	// fixturePayloadDefaultSize is the apparent size of the fixture video.
	// media-scanner ignores video files below its default minimum (5 MiB), so
	// the fixture must exceed that or the grab never imports on a stack using
	// scanner defaults. 6 MiB leaves margin; the tail is sparse (Truncate), so
	// almost no disk or I/O is used.
	fixturePayloadDefaultSize int64 = 6 * 1024 * 1024
)

func fixturePayloadSize(fallback int64) int64 {
	seed := fixtureMediaSeed()
	if seed == "" {
		if fallback < 1 {
			return fixturePayloadDefaultSize
		}
		return fallback
	}
	info, err := os.Stat(seed)
	if err != nil || info.Size() < 1 {
		if fallback < 1 {
			return fixturePayloadDefaultSize
		}
		return fallback
	}
	return info.Size()
}

func materializeFixtureFile(abs string, size int64) error {
	if seed := fixtureMediaSeed(); seed != "" {
		return copyFixtureSeed(seed, abs)
	}
	header := size
	if header > fixtureHeaderSize {
		header = fixtureHeaderSize
	}
	if header < 0 {
		header = 0
	}
	payload := make([]byte, header)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return err
	}
	// Extend to the full apparent size; the tail is a sparse hole of zeros.
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// fixturePayloadReader returns the fixture payload as a stream (seed file when
// configured, else the pattern header followed by a zero tail) and its size.
func fixturePayloadReader(size int64) (io.Reader, int64, func(), error) {
	if seed := fixtureMediaSeed(); seed != "" {
		f, err := os.Open(seed)
		if err != nil {
			return nil, 0, nil, err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, nil, err
		}
		return f, info.Size(), func() { _ = f.Close() }, nil
	}
	header := size
	if header > fixtureHeaderSize {
		header = fixtureHeaderSize
	}
	if header < 0 {
		header = 0
	}
	payload := make([]byte, header)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	r := io.MultiReader(bytes.NewReader(payload), io.LimitReader(zeroReader{}, size-header))
	return r, size, func() {}, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
