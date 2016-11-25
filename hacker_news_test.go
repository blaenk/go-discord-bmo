package main

import "testing"

func TestFormatStory(t *testing.T) {
	story := getHNItem(13027718)

	story.formatStory()
}

func TestFormatComment(t *testing.T) {
	comment := getHNItem(13028891)

	comment.formatComment()
}

func TestDeepFormatComment(t *testing.T) {
	comment := getHNItem(13030726)

	comment.formatComment()
}
