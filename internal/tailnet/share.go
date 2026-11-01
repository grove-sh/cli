package tailnet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const shareFile = "share.json"

// Keep the recorded tailnet and host stable so disconnecting cannot change running servers' URLs.
type Share struct {
	Tailnet string `json:"tailnet"`
	Host    string `json:"host"`
}

func LoadShare(dir string) (share Share, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, shareFile))
	if errors.Is(err, os.ErrNotExist) {
		return Share{}, false, nil
	}
	if err != nil {
		return Share{}, false, err
	}
	if err := json.Unmarshal(raw, &share); err != nil {
		return Share{}, false, fmt.Errorf("tailnet: %s is not a share record: %w", filepath.Join(dir, shareFile), err)
	}
	if share.Tailnet == "" || share.Host == "" {
		return Share{}, false, fmt.Errorf("tailnet: %s names no tailnet or host; run grove share again", filepath.Join(dir, shareFile))
	}
	return share, true, nil
}

func SaveShare(dir string, share Share) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(share, "", "  ")
	if err != nil {
		return err
	}
	// Rename atomically so the daemon cannot read a partially written record.
	tmp := filepath.Join(dir, shareFile+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, shareFile))
}

func RemoveShare(dir string) error {
	err := os.Remove(filepath.Join(dir, shareFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func Zone(domain string, share Share, ok bool) string {
	if !ok {
		return domain
	}
	return share.Host + "." + domain
}
