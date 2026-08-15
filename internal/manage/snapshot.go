package manage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const snapshotVersion = 1

var snapshotIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z$`)

type FileSnapshot struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Mode   uint32 `json:"mode,omitempty"`
	Data   []byte `json:"data,omitempty"`
}

type Snapshot struct {
	Version   int                     `json:"version"`
	ID        string                  `json:"id"`
	CreatedAt time.Time               `json:"createdAt"`
	Sysctls   map[string]string       `json:"sysctls"`
	Files     map[string]FileSnapshot `json:"files"`
}

func captureFile(path string) (FileSnapshot, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return FileSnapshot{Path: path}, nil
	}
	if err != nil {
		return FileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return FileSnapshot{}, fmt.Errorf("refusing to snapshot non-regular file %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileSnapshot{}, err
	}
	return FileSnapshot{Path: path, Exists: true, Mode: uint32(info.Mode().Perm()), Data: data}, nil
}

func writeSnapshot(stateDir string, snapshot Snapshot) error {
	dir := filepath.Join(stateDir, "snapshots", snapshot.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWrite(filepath.Join(dir, "snapshot.json"), data, 0o600); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(stateDir, "latest"), []byte(snapshot.ID+"\n"), 0o600)
}

func readSnapshot(stateDir, id string) (Snapshot, error) {
	if id == "" {
		data, err := os.ReadFile(filepath.Join(stateDir, "latest"))
		if err != nil {
			return Snapshot{}, fmt.Errorf("read latest snapshot: %w", err)
		}
		id = stringTrimSpace(data)
	}
	if !snapshotIDPattern.MatchString(id) {
		return Snapshot{}, fmt.Errorf("invalid snapshot ID")
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "snapshots", id, "snapshot.json"))
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version != snapshotVersion || snapshot.ID != id {
		return Snapshot{}, fmt.Errorf("unsupported or inconsistent snapshot")
	}
	return snapshot, nil
}

func restoreFile(snapshot FileSnapshot) error {
	if !snapshot.Exists {
		if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWrite(snapshot.Path, snapshot.Data, os.FileMode(snapshot.Mode))
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".sshappy-tune-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func stringTrimSpace(data []byte) string {
	start := 0
	end := len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return string(data[start:end])
}
