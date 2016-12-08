package hn

import (
	"fmt"
	"net/url"
	"strconv"

	log "github.com/Sirupsen/logrus"
	"github.com/bwmarrin/discordgo"

	"github.com/blaenk/bmo/bot"
)

// HackerNews is an empty type that implements Previewer.
type HackerNews struct{}

// New creates a new HackerNews instance.
func New() *HackerNews {
	return &HackerNews{}
}

func (hn *HackerNews) previewStory(bot *bot.Bot, item *Item, msg *discordgo.Message, logger *log.Entry) {
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

	_, err := bot.Session().ChannelMessageSendEmbed(msg.ChannelID, embed)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Story embed")
	}

	_, err = bot.Session().ChannelMessageSend(msg.ChannelID, item.URL)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Story target URL")
	}
}

func (hn *HackerNews) previewComment(bot *bot.Bot, item *Item, msg *discordgo.Message, logger *log.Entry) {
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

	_, err = bot.Session().ChannelMessageSendEmbed(msg.ChannelID, embed)

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

	_, err = bot.Session().ChannelMessageSend(msg.ChannelID, commentBody)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Comment body")
	}
}

// Preview sends Discord message embeds previewing any detected Hacker News
// item.
func (hn *HackerNews) Preview(bot *bot.Bot, msg *discordgo.Message, link *url.URL) {
	if link.Host != "news.ycombinator.com" {
		return
	}

	id := link.Query().Get("id")

	hnLog := bot.EmbedLog().WithFields(log.Fields{
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
