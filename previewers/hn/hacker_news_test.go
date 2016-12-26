package hn

import "testing"

func TestFormatStory(t *testing.T) {
	story, err := getHNItem(13027718)

	if err != nil {
		t.Error("Expected no error")
	}
}

func TestFormatComment(t *testing.T) {
	comment, err := getHNItem(13028891)

	if err != nil {
		t.Error("Expected no error")
	}
}

func TestDeepFormatComment(t *testing.T) {
	comment, err := getHNItem(13030726)

	if err != nil {
		t.Error("Expected no error")
	}
}
