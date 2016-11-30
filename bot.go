package main

import (
	"crypto/sha1"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"strconv"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/bwmarrin/dgvoice"
	"github.com/bwmarrin/discordgo"
	ivona "github.com/jpadilla/ivona-go"
	"github.com/mvdan/xurls"
)

func memberFriendlyName(m *discordgo.Member) string {
	if m.Nick != "" {
		return m.Nick
	}

	return m.User.Username
}

func memberDiscordTag(m *discordgo.Member) string {
	return m.User.Username + "#" + m.User.Discriminator
}

// Commander represents a command responder
type Commander interface {
	Command(bot *Bot, message *discordgo.Message)
}

// Bot is a representation of the Bot.
type Bot struct {
	ownerID         string
	userID          string
	ivonaClient     *ivona.Ivona
	lock            sync.Mutex
	session         *discordgo.Session
	voiceStateCache map[string]map[string]*discordgo.VoiceState

	commands   []Commander

	sessionLog *log.Entry
	chatLog    *log.Entry
	voiceLog   *log.Entry
	embedLog   *log.Entry
}

// New creates a new Bot.
func New() *Bot {
	return &Bot{
		// voiceStateCache is a map of GuildIDs to a voiceStateCache which is itself
		// a map of UserIDs to their VoiceState. This mainly facilitates detecting
		// when a user leaves or enters a channel.
		voiceStateCache: map[string]map[string]*discordgo.VoiceState{},

		ivonaClient: ivona.New(os.Getenv("IVONA_ACCESS_KEY"), os.Getenv("IVONA_SECRET_KEY")),

		sessionLog: log.WithField("topic", "session"),
		chatLog:    log.WithField("topic", "chat"),
		voiceLog:   log.WithField("topic", "voice"),
		embedLog:   log.WithField("topic", "embed"),
	}
}

// Close closes the Discord session.
func (b *Bot) Close() error {
	return b.session.Close()
}

// Open opens the Discord session.
func (b *Bot) Open() error {
	var err error

	b.session, err = discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))

	if err != nil {
		b.sessionLog.WithError(err).Fatal("Couldn't establish a Discord session")
	}

	b.registerHandlers()

	return b.session.Open()
}

func (b *Bot) registerHandlers() {
	b.session.AddHandler(b.onReady)
	b.session.AddHandler(b.onMessageUpdate)
	b.session.AddHandler(b.onMessageCreate)
	b.session.AddHandler(b.onVoiceStateUpdate)
}

func (b *Bot) registerCommand(command Commander) {
	b.commands = append(b.commands, command)
}

func (b *Bot) getSelfID() {
	if botUser, err := b.session.User("@me"); err == nil {
		b.userID = botUser.ID
	} else {
		b.sessionLog.WithError(err).Fatal("Couldn't obtain own account details")
	}
}

func (b *Bot) getOwnerID() {
	if ownerUser, err := b.session.User(os.Getenv("BOT_OWNER")); err == nil {
		b.ownerID = ownerUser.ID
	} else {
		b.sessionLog.WithError(err).Fatal("Couldn't obtain owner account details")
	}
}

func (b *Bot) getOrCreateGuildVoiceStateCache(guildID string) map[string]*discordgo.VoiceState {
	if guildVoiceStateCache, ok := b.voiceStateCache[guildID]; ok {
		return guildVoiceStateCache
	}

	b.voiceStateCache[guildID] = map[string]*discordgo.VoiceState{}
	return b.voiceStateCache[guildID]
}

func (b *Bot) setupGuild(guild *discordgo.Guild) {
	// Populate voiceStateCache
	guildVoiceStateCache := b.getOrCreateGuildVoiceStateCache(guild.ID)

	for _, voiceState := range guild.VoiceStates {
		voiceState.GuildID = guild.ID
		guildVoiceStateCache[voiceState.UserID] = voiceState
	}

	b.voiceStateCache[guild.ID] = guildVoiceStateCache
}

func (b *Bot) onReady(_ *discordgo.Session, event *discordgo.Ready) {
	b.sessionLog.Info("Connection is ready")

	b.getSelfID()
	b.getOwnerID()

	for _, guild := range event.Guilds {
		if !guild.Unavailable {
			b.setupGuild(guild)
		}
	}
}

func (b *Bot) onGuildCreate(_ *discordgo.Session, guild *discordgo.GuildCreate) {
	b.setupGuild(guild.Guild)
}

// IsSelf checks if the ID is the bot's ID.
func (b *Bot) IsSelf(ID string) bool {
	return b.userID == ID
}

// IsOwner checks if the ID is the bot's owner's ID.
func (b *Bot) IsOwner(ID string) bool {
	return b.ownerID == ID
}

// CanIssueCommands checks whether the user with the given ID can issue commands.
func (b *Bot) CanIssueCommands(ID string) bool {
	return b.IsOwner(ID)
}

func (b *Bot) onMessageUpdate(_ *discordgo.Session, msg *discordgo.MessageUpdate) {
	// Detect message updates here
}

func (b *Bot) onMessageCreate(_ *discordgo.Session, msg *discordgo.MessageCreate) {
	// Ignore messages we created.
	if b.IsSelf(msg.Author.ID) {
		b.chatLog.Info("Skipping self message")
		return
	}

	b.chatLog.Info("Received message from owner")

	b.previewURLs(msg.Message)

	// TODO
	// Allow more granular permissions later.
	if b.CanIssueCommands(msg.Author.ID) {
		for _, command := range b.commands {
			command.Command(b, msg.Message)
		}
	}
}

func (b *Bot) previewURLs(msg *discordgo.Message) {
	b.chatLog.Info("Previewing URLs")

	// It seems like the first time Discord encounters a unique URL it doesn't
	// immediately appear as an Embed.
	//
	// It seems that in order to avoid a delay from parsing out URLs before
	// relaying the message, Discord looks for URLs asynchronously then it edits
	// the message once/if any URLs are found.
	//
	// The problem with simply adding a handler for the MessageUpdate event is
	// that we wouldn't be able to distinguish new URLs from those we've already
	// previewed unless we maintained a record.
	//
	// We avoid those issues and simply detect URLs ourselves.

	for _, link := range xurls.Relaxed.FindAllString(msg.Content, -1) {
		parsed, err := url.Parse(link)

		if err != nil {
			b.chatLog.WithError(err).Errorln("Couldn't parse URL:", link)
			continue
		}

		// TODO
		// Create an interface for previewers and command responders.

		b.previewHackerNews(msg, parsed)
	}
}

func (b *Bot) previewHNStory(item *Item, msg *discordgo.Message, logger *log.Entry) {
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

	_, err := b.session.ChannelMessageSendEmbed(msg.ChannelID, embed)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Story embed")
	}

	_, err = b.session.ChannelMessageSend(msg.ChannelID, item.URL)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Story target URL")
	}
}

func (b *Bot) previewHNComment(item *Item, msg *discordgo.Message, logger *log.Entry) {
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

	_, err = b.session.ChannelMessageSendEmbed(msg.ChannelID, embed)

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

	_, err = b.session.ChannelMessageSend(msg.ChannelID, commentBody)

	if err != nil {
		logger.WithError(err).Error("Couldn't send HN Comment body")
	}
}

func (b *Bot) previewHackerNews(msg *discordgo.Message, link *url.URL) {
	if link.Host != "news.ycombinator.com" {
		return
	}

	id := link.Query().Get("id")

	hnLog := b.embedLog.WithFields(log.Fields{
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
		b.previewHNStory(item, msg, hnLog)

	case "comment":
		b.previewHNComment(item, msg, hnLog)

	default:
		hnLog.Warn("Unknown HN item type")
	}
}

func (b *Bot) getIvonaSpeech(text string) (string, error) {
	shaSum := fmt.Sprintf("%x", sha1.Sum([]byte(text)))
	speechPath := path.Join("./data/speech", shaSum)

	if _, err := os.Stat(speechPath); err == nil {
		b.voiceLog.Infoln("Cache Hit: Ivona Speech:", text)
		return speechPath, nil
	}

	b.voiceLog.Info("Cache Miss: Ivona Speech")

	speechOptions := ivona.NewSpeechOptions(text)
	response, err := b.ivonaClient.CreateSpeech(speechOptions)

	if err != nil {
		b.voiceLog.WithError(err).Error("Ivona Speech request failure")
		return "", err
	}

	if ioutil.WriteFile(speechPath, response.Audio, 0644) != nil {
		b.voiceLog.WithError(err).Error("Couldn't cache Ivona Speech")
		return "", err
	}

	return speechPath, nil
}

func (b *Bot) ivonaSpeak(voiceConnection *discordgo.VoiceConnection, text string) error {
	// TODO
	// This needs to be queued up as events that specify what to say in what channel.
	// Currently switching between different channels while it's talking causes
	// distortions, presumably because speaking happens in another goroutine so
	// that the bot ends up switching channels mid-transmission.
	//
	// Instead it shouldn't switch channels until the transmission is complete.
	//
	// Look into the possibility of pausing certain audio transmissions, switching
	// channels, then switching back and resuming.

	if speechFile, err := b.getIvonaSpeech(text); err == nil {
		dgvoice.PlayAudioFile(voiceConnection, speechFile)
	} else {
		return err
	}

	return nil
}

func channelName(guild *discordgo.Guild, channel *discordgo.Channel) string {
	return guild.Name + "#" + channel.Name + "[" + channel.Type + "]"
}

func (b *Bot) speakPresenceUpdate(voiceState *discordgo.VoiceState, action string) {
	voiceConnection, err := b.session.ChannelVoiceJoin(
		voiceState.GuildID, voiceState.ChannelID,
		false, true)

	member, err := b.session.State.Member(voiceState.GuildID, voiceState.UserID)

	if err != nil {
		b.sessionLog.WithFields(log.Fields{
			"guild": voiceState.GuildID,
			"user":  voiceState.UserID,
		}).WithError(err).Error("Couldn't find user")

		return
	}

	presenceText := fmt.Sprintf("%s %s the channel", memberFriendlyName(member), action)

	if err := b.ivonaSpeak(voiceConnection, presenceText); err != nil {
		b.sessionLog.WithError(err).Error("Couldn't speak with Ivona")
	}
}

func (b *Bot) logger(logger *log.Entry, args ...interface{}) *log.Entry {
	for _, arg := range args {
		if arg == nil {
			continue
		}

		switch t := arg.(type) {
		case *discordgo.Guild:
			logger = logger.WithField("guild", t.Name)

		case *discordgo.Channel:
			logger = logger.WithField("channel", t.Name)

		case *discordgo.Member:
			logger = logger.WithField("user", memberDiscordTag(t))
		}
	}

	return logger
}

func (b *Bot) voiceStateLog(voiceState *discordgo.VoiceState) *log.Entry {
	logger := b.voiceLog

	if guild, err := b.session.State.Guild(voiceState.GuildID); err == nil {
		logger = logger.WithField("guild", guild.Name)
	} else {
		b.sessionLog.WithField("guild", voiceState.GuildID).WithError(err).Error("Couldn't find guild")
		logger = logger.WithField("guild", voiceState.GuildID)
	}

	if member, err := b.session.State.Member(voiceState.GuildID, voiceState.UserID); err == nil {
		logger = logger.WithField("user", memberDiscordTag(member))
	} else {
		b.sessionLog.WithField("user", voiceState.UserID).WithError(err).Error("Couldn't find user")
		logger = logger.WithField("user", voiceState.UserID)
	}

	if channel, err := b.session.State.Channel(voiceState.ChannelID); err == nil {
		logger = logger.WithField("channel", channel.Name)
	} else {
		b.sessionLog.WithField("channel", voiceState.ChannelID).WithError(err).Error("Couldn't find channel")
		logger = logger.WithField("channel", voiceState.ChannelID)
	}

	return logger
}

func (b *Bot) onUserLeaveVoiceChannel(voiceState *discordgo.VoiceState) {
	b.voiceStateLog(voiceState).Info("User left")

	b.speakPresenceUpdate(voiceState, "left")
}

func (b *Bot) onUserJoinVoiceChannel(voiceState *discordgo.VoiceState) {
	b.voiceStateLog(voiceState).Info("User joined")

	b.speakPresenceUpdate(voiceState, "joined")
}

func (b *Bot) announceVoiceStateUpdate(update *discordgo.VoiceState) {
	guildVoiceStateCache := b.getOrCreateGuildVoiceStateCache(update.GuildID)

	if cached, wasCached := guildVoiceStateCache[update.UserID]; wasCached {
		changedChannels := cached.ChannelID != update.ChannelID

		if !changedChannels {
			b.voiceLog.Info("No channel change detected")
			return
		}

		leftChannel := cached.ChannelID != ""

		if leftChannel {
			b.onUserLeaveVoiceChannel(cached)
		}

		delete(guildVoiceStateCache, update.UserID)
	}

	joinedChannel := update.ChannelID != ""

	if joinedChannel {
		b.onUserJoinVoiceChannel(update)

		guildVoiceStateCache[update.UserID] = update
	}
}

func (b *Bot) onVoiceStateUpdate(_ *discordgo.Session, update *discordgo.VoiceStateUpdate) {
	if b.IsSelf(update.UserID) {
		b.voiceLog.Info("Ignoring bot VoiceStateUpdate")
		return
	}

	b.announceVoiceStateUpdate(update.VoiceState)
}
