package internal

import (
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

func fixturePayloadSize(fallback int64) int64 {
	seed := fixtureMediaSeed()
	if seed == "" {
		if fallback < 1 {
			return 8 * 1024
		}
		return fallback
	}
	info, err := os.Stat(seed)
	if err != nil || info.Size() < 1 {
		if fallback < 1 {
			return 8 * 1024
		}
		return fallback
	}
	return info.Size()
}

func materializeFixtureFile(abs string, size int64) error {
	if seed := fixtureMediaSeed(); seed != "" {
		return copyFixtureSeed(seed, abs)
	}
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	return os.WriteFile(abs, payload, 0o644)
}
