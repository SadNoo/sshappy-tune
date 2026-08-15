package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/SadNoo/sshappy-tune/internal/tune"
)

const (
	profileVersion = 1
	managedBy      = "sshappy-tune"
)

type Profile struct {
	Version   int        `json:"version"`
	ManagedBy string     `json:"managedBy"`
	Input     tune.Input `json:"input"`
}

func NewProfile(input tune.Input) Profile {
	if input.Role == "" {
		input.Role = "proxy"
	}
	return Profile{Version: profileVersion, ManagedBy: managedBy, Input: input}
}

func LoadProfile(path string) (Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	var profile Profile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("parse profile: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Profile{}, fmt.Errorf("parse profile: trailing JSON content")
		}
		return Profile{}, fmt.Errorf("parse profile: %w", err)
	}
	if profile.Version != profileVersion || profile.ManagedBy != managedBy {
		return Profile{}, fmt.Errorf("unsupported or unmanaged profile")
	}
	return profile, nil
}

func renderProfile(profile Profile) ([]byte, error) {
	if profile.Version != profileVersion || profile.ManagedBy != managedBy {
		return nil, fmt.Errorf("invalid managed profile")
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
