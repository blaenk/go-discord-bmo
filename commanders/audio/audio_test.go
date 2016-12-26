package audio

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAudioMetadata(t *testing.T) {
	meta, err := GetAudioMetadata("https://soundcloud.com/dofordadubstep/vaporwave")

	assert.Nil(t, err)

	assert.Equal(t, meta.Origin, "https://soundcloud.com/dofordadubstep/vaporwave")

	if meta.AudioURL == "" {
		t.Error("Expected present audioURL")
	}

	assert.Equal(t, meta.Title, "vaporwave")
}

func TestIncorrectAudioOrigin(t *testing.T) {
	_, err := GetAudioMetadata("https://www.youtube.com")

	assert.NotNil(t, err)
}
