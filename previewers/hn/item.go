package hn

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"

	log "github.com/Sirupsen/logrus"
)

// Item is a representation of a Hacker News story or comment
type Item struct {
	Author      string `json:"by"`
	Body        string `json:"text"`
	ID          int    `json:"id"`
	Parent      int    `json:"parent"`
	Descendants int    `json:"descendants"`
	Kids        []int  `json:"kids"`
	Score       int    `json:"score"`
	Time        int64  `json:"time"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	URL         string `json:"url"`
}

func getHNItem(id int) (*Item, error) {
	url := fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%d.json", id)

	logger := log.WithFields(log.Fields{
		"topic": "HN",
		"ID":    id,
	})

	resp, err := http.Get(url)

	if err != nil {
		logger.WithError(err).Error("Couldn't get item")
		return nil, err
	}

	decoder := json.NewDecoder(resp.Body)
	defer resp.Body.Close()

	item := &Item{}

	if err = decoder.Decode(&item); err != nil {
		logger.WithError(err).Error("Couldn't parse JSON")
		return nil, err
	}

	return item, nil
}

func (i *Item) formatTime() string {
	t := time.Unix(i.Time, 0)
	format := "3:04pm MST on Monday, January 2"

	// TODO
	// Allow timezone to be set via environment variable. If UTC Is chosen then we
	// don't need to load the location.

	pst, err := time.LoadLocation("America/Los_Angeles")

	if err != nil {
		log.WithFields(log.Fields{
			"topic": "HN",
			"id":    i.ID,
		}).WithError(err).Error("Couldn't load America/Los_Angeles")

		return t.UTC().Format(format)
	}

	return t.In(pst).Format(format)
}

func (i *Item) formatCommentBody() (string, error) {
	doc, err := html.Parse(strings.NewReader(i.Body))

	if err != nil {
		log.WithFields(log.Fields{
			"topic": "HN",
			"id":    i.ID,
		}).WithError(err).Error("Couldn't parse HTML")

		return "", err
	}

	v := &Visitor{}

	v.Visit(doc)

	return v.CollectedText(), nil
}

func (i *Item) itemURL() string {
	return fmt.Sprintf("https://news.ycombinator.com/item?id=%d", i.ID)
}

func (i *Item) authorURL() string {
	return fmt.Sprintf("https://news.ycombinator.com/user?id=%s", i.Author)
}

func (i *Item) findRoot() (*Item, error) {
	logger := log.WithFields(log.Fields{
		"topic": "HN",
		"ID":    i.ID,
	})

	parent, err := getHNItem(i.Parent)

	if err != nil {
		logger.WithField("parent", i.Parent).WithError(err).Error("Couldn't get parent")
		return nil, err
	}

	switch parent.Type {
	case "story":
		return parent, nil

	case "comment":
		return parent.findRoot()

	default:
		err := fmt.Errorf("Unknown type")

		logger.WithFields(log.Fields{
			"type": parent.Type,
		}).WithError(err).Error("Unknown HN item type")

		return nil, err
	}
}
