package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// import "text/template"

// Visitor is a Hacker News comment body HTML Visitor
type Visitor struct {
	buffer bytes.Buffer
}

func (v *Visitor) visitText(node *html.Node) {
	v.buffer.WriteString(node.Data)
}

func (v *Visitor) visitLink(node *html.Node) {
	for _, attr := range node.Attr {
		if attr.Key == "href" {
			v.buffer.WriteString(attr.Val)
			break
		}
	}
}

func (v *Visitor) visitParagraph(node *html.Node) {
	v.buffer.WriteString("\n\n")

	v.visitChildren(node)
}

func (v *Visitor) visitCode(node *html.Node) {
	v.buffer.WriteString("```\n")

	v.visitChildren(node)

	v.buffer.WriteString("```")
}

func (v *Visitor) visitItalic(node *html.Node) {
	v.buffer.WriteString("*")

	v.visitChildren(node)

	v.buffer.WriteString("*")
}

func (v *Visitor) visitElement(node *html.Node) {
	switch node.Data {
	case "a":
		v.visitLink(node)
	case "p":
		v.visitParagraph(node)
	case "code":
		v.visitCode(node)
	case "i":
		v.visitItalic(node)
	default:
		v.visitChildren(node)
	}
}

func (v *Visitor) visitChildren(node *html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		v.Visit(child)
	}
}

// Visit traverses the HTML document structure, converting the content to
// Discord's Markdown-ish format as it goes.
func (v *Visitor) Visit(node *html.Node) {
	if node == nil {
		return
	}

	switch node.Type {
	case html.DocumentNode:
		v.visitChildren(node)
	case html.TextNode:
		v.visitText(node)
	case html.ElementNode:
		v.visitElement(node)
	}
}

// CollectedText produces the Discord Markdown-ish content.
func (v *Visitor) CollectedText() string {
	return v.buffer.String()
}

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

type HackerNews struct{}

func (hn *HackerNews) previewStory(bot *Bot, item *Item, msg *discordgo.Message, logger *log.Entry) {
	description := fmt.Sprintf("**%d** points. **%d** comments", item.Score, item.Descendants)

	embed := &discordgo.MessageEmbed{
		URL:         item.itemURL(),
		Type:        "article",
		Title:       item.Title,
		Description: description,
		Color:       0xff6600,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL:    "https://news.ycombinator.com/y18.gif",
			Width:  32,
			Height: 32,
		},
		Provider: &discordgo.MessageEmbedProvider{
			URL:  "https://news.ycombinator.com",
			Name: "Hacker News",
		},
	}

	_, err := bot.session.ChannelMessageSendEmbed(msg.ChannelID, embed)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Story embed")
	}

	_, err = bot.session.ChannelMessageSend(msg.ChannelID, item.URL)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Story target URL")
	}
}

func (hn *HackerNews) previewComment(bot *Bot, item *Item, msg *discordgo.Message, logger *log.Entry) {
	root, err := item.findRoot()

	if err != nil {
		logger.WithError(err).Error("Couldn't find root")
		return
	}

	var description string

	if len(item.Kids) == 0 {
		description = fmt.Sprintf(`by **%s**`, item.Author)
	} else {
		description = fmt.Sprintf("**%d** replies. by **%s**", len(item.Kids), item.Author)
	}

	const HackerNewsOrange int = 0xff6600

	embed := &discordgo.MessageEmbed{
		URL:         item.itemURL(),
		Type:        "article",
		Title:       "Comment on: " + root.Title,
		Description: description,
		Color:       HackerNewsOrange,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: "https://news.ycombinator.com/y18.gif",
		},
	}

	_, err = bot.session.ChannelMessageSendEmbed(msg.ChannelID, embed)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Comment embed")
	}

	formattedBody, err := item.formatCommentBody()

	if err != nil {
		logger.WithError(err).Error("Couldn't parse HN Comment body HTML")
		return
	}

	commentBody := fmt.Sprintf(`:speech_left: **BEGIN QUOTE** :speech_balloon:
%s
:speech_left: **END QUOTE** :speech_balloon:`, formattedBody)

	_, err = bot.session.ChannelMessageSend(msg.ChannelID, commentBody)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Comment body")
	}
}

func (hn *HackerNews) Preview(bot *Bot, msg *discordgo.Message, link *url.URL) {
	if link.Host != "news.ycombinator.com" {
		return
	}

	id := link.Query().Get("id")

	hnLog := bot.embedLog.WithFields(log.Fields{
		"preview": "HN",
		"id":      id,
	})

	intID, err := strconv.Atoi(id)

	if err != nil {
		hnLog.WithError(err).Error("Couldn't parse ID as int")
		return
	}

	item, err := getHNItem(intID)

	if err != nil {
		hnLog.WithError(err).Error("Couldn't get item")
		return
	}

	hnLog = hnLog.WithField("type", item.Type)

	switch item.Type {
	case "story":
		hn.previewStory(bot, item, msg, hnLog)

	case "comment":
		hn.previewComment(bot, item, msg, hnLog)

	default:
		hnLog.Warn("Unknown HN item type")
	}
}
