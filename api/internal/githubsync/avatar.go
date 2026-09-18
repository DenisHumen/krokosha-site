package githubsync

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif" // register decoders for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The avatar is downloaded as is and served locally, never hotlinked (brief B3). Conversion to
// AVIF / WebP happens in the site build, which already ships an image encoder (sharp).

const (
	maxAvatarBytes = 5 << 20
	minAvatarSide  = 64
)

var avatarExtensions = []string{"png", "jpg", "gif", "webp"}

type avatarState struct {
	Source string `json:"source"`
	ETag   string `json:"etag"`
	File   string `json:"file"`
}

// sniffImage recognises the formats the site build can read and returns the file extension.
func sniffImage(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png", nil
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "jpg", nil
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif", nil
	case len(data) > 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp", nil
	}
	return "", fmt.Errorf("not a PNG, JPEG, GIF or WebP image (%d bytes)", len(data))
}

// validateImage makes sure a broken download can never reach the site build, where it would
// stop every following release.
func validateImage(data []byte) (string, error) {
	extension, err := sniffImage(data)
	if err != nil {
		return "", err
	}
	if extension == "webp" {
		return extension, nil // no decoder in the standard library; the header check has to do
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("corrupted %s image: %w", extension, err)
	}
	if config.Width < minAvatarSide || config.Height < minAvatarSide {
		return "", fmt.Errorf("image is too small: %d×%d", config.Width, config.Height)
	}
	return extension, nil
}

// syncAvatar downloads the avatar when it has changed. It returns true if a new file was written.
func (s *syncer) syncAvatar(ctx context.Context, state *State) (bool, error) {
	source := s.opts.Content.Site.Profile.AvatarSource
	if source == "" {
		return false, nil
	}
	if !strings.HasPrefix(source, "https://") {
		return false, fmt.Errorf("avatar_source must be an https:// URL, got %q", source)
	}
	return s.downloadAvatar(ctx, state, source)
}

func (s *syncer) downloadAvatar(ctx context.Context, state *State, source string) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("User-Agent", userAgent)
	current := filepath.Join(s.opts.OutDir, state.Avatar.File)
	if state.Avatar.Source == source && state.Avatar.ETag != "" && state.Avatar.File != "" && fileExists(current) {
		request.Header.Set("If-None-Match", state.Avatar.ETag)
	}

	reply, err := s.client.http.Do(request)
	if err != nil {
		return false, err
	}
	defer reply.Body.Close()
	if reply.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if reply.StatusCode != http.StatusOK {
		return false, &statusError{status: reply.StatusCode, url: source}
	}
	data, err := io.ReadAll(io.LimitReader(reply.Body, maxAvatarBytes+1))
	if err != nil {
		return false, err
	}
	if len(data) > maxAvatarBytes {
		return false, fmt.Errorf("avatar is larger than %d bytes", maxAvatarBytes)
	}
	extension, err := validateImage(data)
	if err != nil {
		return false, fmt.Errorf("avatar: %w", err)
	}

	file := "avatar." + extension
	if err := writeFileAtomic(filepath.Join(s.opts.OutDir, file), data); err != nil {
		return false, err
	}
	// The site build takes the first avatar.* it finds: leave exactly one.
	for _, other := range avatarExtensions {
		if other != extension {
			_ = os.Remove(filepath.Join(s.opts.OutDir, "avatar."+other))
		}
	}
	state.Avatar = avatarState{Source: source, ETag: reply.Header.Get("ETag"), File: file}
	return true, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
