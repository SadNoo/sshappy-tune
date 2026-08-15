package automation

import (
	"fmt"
	"os"
	"path/filepath"
)

type ownedFile struct {
	Path   string
	Exists bool
	Mode   os.FileMode
	Data   []byte
}

func captureOwnedFile(path string, validate func([]byte) bool) (ownedFile, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ownedFile{Path: path}, nil
	}
	if err != nil {
		return ownedFile{}, err
	}
	if !info.Mode().IsRegular() {
		return ownedFile{}, fmt.Errorf("refusing to replace non-regular path %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ownedFile{}, err
	}
	if !validate(data) {
		return ownedFile{}, fmt.Errorf("refusing to replace file not owned by sshappy-tune: %s", path)
	}
	return ownedFile{Path: path, Exists: true, Mode: info.Mode().Perm(), Data: data}, nil
}

func restoreOwnedFile(file ownedFile) error {
	if !file.Exists {
		if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWrite(file.Path, file.Data, file.Mode)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".sshappy-tune-automation-*")
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
