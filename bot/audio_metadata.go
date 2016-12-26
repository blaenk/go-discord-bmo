package bot

import (
	"fmt"
	"os/exec"
	"strings"
)

type AudioMetadata struct {
	Origin   string
	AudioURL string
	Title    string
}

func GetAudioMetadata(url string) (*AudioMetadata, error) {
	out, err := exec.Command(
		"youtube-dl",
		"--get-title",
		"--get-url",
		"--format",
		"bestaudio",
		url,
	).Output()

	audio := &AudioMetadata{}

	if err != nil {
		return audio, err
	}

	trimmed := strings.TrimSpace(string(out))
	components := strings.Split(trimmed, "\n")

	if len(components) != 2 {
		return audio, fmt.Errorf("Expected two components (title, url) got: %+v", components)
	}

	audio.Origin = url
	audio.Title = components[0]
	audio.AudioURL = components[1]

	return audio, nil
}
