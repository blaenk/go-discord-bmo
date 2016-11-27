package main

import (
	"crypto/sha1"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"strconv"
	"sync"

	"github.com/bwmarrin/dgvoice"
	"github.com/bwmarrin/discordgo"
	ivona "github.com/jpadilla/ivona-go"
	"github.com/mvdan/xurls"
)

// Bot is a representation of the Bot.
type Bot struct {
	ownerID         string
	userID          string
	ivonaClient     *ivona.Ivona
	lock            sync.Mutex
	session         *discordgo.Session
	voiceStateCache map[string]*discordgo.VoiceState
}

// New creates a new Bot.
func New() *Bot {
	return &Bot{
		voiceStateCache: make(map[string]*discordgo.VoiceState),
		ivonaClient:     ivona.New(os.Getenv("IVONA_ACCESS_KEY"), os.Getenv("IVONA_SECRET_KEY")),
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
		log.Fatal("Couldn't establish a Discord session", err)
	}

	b.registerHandlers()

	return b.session.Open()
}

func (b *Bot) registerHandlers() {
	b.session.AddHandler(b.onReady)
	b.session.AddHandler(b.onMessageCreate)
	b.session.AddHandler(b.onVoiceStateUpdate)
}

func (b *Bot) getSelfID() {
	if botUser, err := b.session.User("@me"); err == nil {
		b.userID = botUser.ID
	} else {
		log.Fatal("Couldn't obtain account details", err)
	}
}

func (b *Bot) getOwnerID() {
	if ownerUser, err := b.session.User(os.Getenv("BOT_OWNER")); err == nil {
		b.ownerID = ownerUser.ID
	} else {
		log.Fatal("Couldn't obtain account details", err)
	}
}

func (b *Bot) onReady(_ *discordgo.Session, event *discordgo.Ready) {
	log.Println("Connection is ready")

	b.getSelfID()
	b.getOwnerID()
}

// IsSelf checks if the ID is the bot's ID.
func (b *Bot) IsSelf(ID string) bool {
	return b.userID == ID
}

// IsOwner checks if the ID is the bot's owner's ID.
func (b *Bot) IsOwner(ID string) bool {
	return b.ownerID == ID
}

func (b *Bot) onMessageCreate(_ *discordgo.Session, msg *discordgo.MessageCreate) {
	// Ignore messages we created.
	if b.IsSelf(msg.Author.ID) {
		log.Println("Skipping self message:\n", msg.Content)
		return
	}

	b.previewURLs(msg.Message)

	if b.IsOwner(msg.Author.ID) {
		log.Println("Received message from owner")

		b.respondToPing(msg.Message)
	}
}

func (b *Bot) respondToPing(msg *discordgo.Message) {
	if msg.Content == "ping" {
		_, _ = b.session.ChannelMessageSend(msg.ChannelID, "Pong!")
	}
}

func (b *Bot) previewURLs(msg *discordgo.Message) {
	log.Println("Previewing URLs")

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
			log.Println("Couldn't parse URL:", link, err)
		}

		b.previewHackerNews(msg, parsed)
	}
}

func (b *Bot) previewHackerNews(msg *discordgo.Message, link *url.URL) {
	if link.Host != "news.ycombinator.com" {
		return
	}

	id := link.Query().Get("id")

	intID, err := strconv.Atoi(id)

	if err != nil {
		log.Println("Couldn't convert ID", id, "to int:", err)
		return
	}

	item := getHNItem(intID)

	_, _ = b.session.ChannelMessageSend(msg.ChannelID, item.Format())
}

func (b *Bot) getIvonaSpeech(text string) (string, error) {
	shaSum := fmt.Sprintf("%x", sha1.Sum([]byte(text)))
	speechPath := path.Join("./data/speech", shaSum)

	if _, err := os.Stat(speechPath); err == nil {
		log.Println("Found cached speech for:", text)

		return speechPath, nil
	}

	log.Println("No cached speech found; requesting from Ivona.")

	speechOptions := ivona.NewSpeechOptions(text)
	response, err := b.ivonaClient.CreateSpeech(speechOptions)

	if err != nil {
		return "", err
	}

	if ioutil.WriteFile(speechPath, response.Audio, 0644) != nil {
		return "", err
	}

	return speechPath, nil
}

func (b *Bot) ivonaSpeak(voiceConnection *discordgo.VoiceConnection, text string) error {
	if speechFile, err := b.getIvonaSpeech(text); err == nil {
		dgvoice.PlayAudioFile(voiceConnection, speechFile)
	} else {
		return err
	}

	return nil
}

func (b *Bot) speakPresenceUpdate(voiceState *discordgo.VoiceState, action string) {
	log.Println("User", voiceState.UserID, action, "channel", voiceState.ChannelID)

	voiceConnection, err := b.session.ChannelVoiceJoin(voiceState.GuildID, voiceState.ChannelID, false, true)

	user, err := b.session.User(voiceState.UserID)

	if err != nil {
		log.Fatal("Can't find user", voiceState.UserID)
	}

	presenceText := fmt.Sprintf("%s %s the channel", user.Username, action)

	if err := b.ivonaSpeak(voiceConnection, presenceText); err != nil {
		log.Println("Couldn't speak with Ivona:", err)
	}
}

func (b *Bot) onVoiceStateUpdate(_ *discordgo.Session, update *discordgo.VoiceStateUpdate) {
	if b.IsSelf(update.UserID) {
		log.Println("Ignoring bot's VoiceStateUpdate")
		return
	}

	if cached, ok := b.voiceStateCache[update.UserID]; ok {
		changedChannels := cached.ChannelID != update.ChannelID

		if !changedChannels {
			log.Println("No channel change detected")
			return
		}

		leftChannel := cached.ChannelID != ""

		if leftChannel {
			b.speakPresenceUpdate(cached, "left")
		}
	}

	joinedChannel := update.ChannelID != ""

	if joinedChannel {
		b.speakPresenceUpdate(update.VoiceState, "joined")
	}

	b.voiceStateCache[update.UserID] = update.VoiceState
}
