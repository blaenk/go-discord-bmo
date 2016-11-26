package main

import (
	"fmt"
	"github.com/bwmarrin/dgvoice"
	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"github.com/jpadilla/ivona-go"
	// "github.com/layeh/gopus"
	"crypto/sha1"
	"github.com/mvdan/xurls"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"sync"
)

// Bot is a representation of the Bot.
type Bot struct {
	lock            sync.Mutex
	OwnerID         string
	UserID          string
	VoiceStateCache map[string]*discordgo.VoiceState
}

func NewBot(UserID, OwnerID string) *Bot {
	return &Bot{
		OwnerID:         OwnerID,
		UserID:          UserID,
		VoiceStateCache: make(map[string]*discordgo.VoiceState),
	}
}

var BMO *Bot

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	sess, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))

	if err != nil {
		log.Fatal("Couldn't establish a Discord session", err)
	}

	botUser, err := sess.User("@me")

	if err != nil {
		log.Fatal("Couldn't obtain account details", err)
	}

	ownerUser, err := sess.User(os.Getenv("BOT_OWNER"))

	BMO = NewBot(botUser.ID, ownerUser.ID)

	sess.AddHandler(Ready)
	sess.AddHandler(MessageCreate)
	sess.AddHandler(VoiceStateUpdate)

	err = sess.Open()
	defer sess.Close()

	if err != nil {
		log.Fatal("Couldn't open a connection to Discord", err)
	}

	log.Println("Bot is now running")

	// Listen for SIGINT and gracefully disconnect.
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt)

	<-signalChannel
}

func Ready(sess *discordgo.Session, event *discordgo.Ready) {
	log.Println("Connection is ready")
}

func IvonaSpeak(text string) (string, error) {
	ivonaClient := ivona.New(os.Getenv("IVONA_ACCESS_KEY"), os.Getenv("IVONA_SECRET_KEY"))
	speechOptions := ivona.NewSpeechOptions(text)

	response, err := ivonaClient.CreateSpeech(speechOptions)

	if err != nil {
		return "", err
	}

	shaSum := fmt.Sprintf("%x", sha1.Sum([]byte(text)))

	speechPath := path.Join("./data/speech", shaSum)

	if ioutil.WriteFile(speechPath, response.Audio, 0644) != nil {
		return "", err
	}

	return speechPath, nil
}

func VoiceStateUpdate(sess *discordgo.Session, update *discordgo.VoiceStateUpdate) {
	if update.UserID == BMO.UserID {
		log.Println("Ignoring bot VoiceStateUpdate")
		return
	}

	log.Printf("VoiceStateUpdate: %+v", update.VoiceState)

	cached, ok := BMO.VoiceStateCache[update.UserID]

	if ok && cached.ChannelID != update.ChannelID && cached.ChannelID != "" {
		log.Println("User", cached.UserID, "left channel", cached.ChannelID)
	}

	if update.ChannelID != "" {
		log.Println("User", update.UserID, "joined channel", update.ChannelID)

		user, err := sess.User(update.UserID)

		if err != nil {
			log.Fatal("Can't find user", update.UserID)
		}

		speechFile, err := IvonaSpeak(fmt.Sprintf("%s joined the channel", user.Username))

		voiceConnection, err := sess.ChannelVoiceJoin(update.GuildID, update.ChannelID, false, true)

		dgvoice.PlayAudioFile(voiceConnection, speechFile)
	}

	BMO.VoiceStateCache[update.UserID] = update.VoiceState
}

func MessageCreate(sess *discordgo.Session, msg *discordgo.MessageCreate) {
	// Ignore messages we created.
	if msg.Author.ID == BMO.UserID {
		log.Println("Skipping self message:\n", msg.Content)
		return
	}

	// if msg.Author.ID == os.Getenv("BOT_OWNER") {
	if msg.Author.ID == BMO.OwnerID {
		log.Println("Received message from owner")

		if msg.Content == "ping" {
			_, _ = sess.ChannelMessageSend(msg.ChannelID, "Pong!")
		}

		if msg.Content == "disconnect" {
			_, _ = sess.ChannelMessageSend(msg.ChannelID, "Disconnecting!")
			sess.Close()
		}
	}

	for _, link := range xurls.Relaxed.FindAllString(msg.Content, -1) {
		parsed, err := url.Parse(link)

		if err != nil {
			log.Println("Couldn't parse link:", link, err)
		}

		switch parsed.Host {
		case "news.ycombinator.com":
			id := parsed.Query().Get("id")

			intID, err := strconv.Atoi(id)

			if err != nil {
				log.Println("Couldn't convert ID", id, "to int:", err)
				break
			}

			item := getHNItem(intID)

			_, _ = sess.ChannelMessageSend(msg.ChannelID, item.Format())
		}
	}
}
