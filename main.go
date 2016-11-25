package main

import (
	// "fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"github.com/mvdan/xurls"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
)

var (
	BotID string
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	discord, err := discordgo.New("Bot " + os.Getenv("DISCORD_TOKEN"))

	if err != nil {
		log.Fatal("Couldn't establish a Discord session", err)
	}

	bot, err := discord.User("@me")

	BotID = bot.ID

	if err != nil {
		log.Fatal("Couldn't obtain account details", err)
	}

	discord.AddHandler(messageCreate)

	err = discord.Open()
	defer discord.Close()

	if err != nil {
		log.Fatal("Couldn't open a connection to Discord", err)
	}

	log.Println("Bot is now running")

	// Listen for SIGINT and gracefully disconnect.
	go func() {
		signalChannel := make(chan os.Signal, 1)
		signal.Notify(signalChannel, os.Interrupt)

		<-signalChannel

		discord.Close()

		os.Exit(0)
	}()

	<-make(chan struct{})
}

func messageCreate(sess *discordgo.Session, msg *discordgo.MessageCreate) {
	// Ignore messages we created.
	if msg.Author.ID == BotID {
		log.Println("Skipping self message:\n", msg.Content)
		return
	}

	if msg.Author.ID == os.Getenv("BOT_OWNER") {
		log.Println("Received message from owner")

		if msg.Content == "ping" {
			_, _ = sess.ChannelMessageSend(msg.ChannelID, "Pong!")
		}

		if msg.Content == "disconnect" {
			_, _ = sess.ChannelMessageSend(msg.ChannelID, "Disconnecting!")
			sess.Close()
		}
	}

	urls := xurls.Relaxed.FindAllString(msg.Content, -1)

	for _, link := range urls {
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
			}

			item := getHNItem(intID)

			_, _ = sess.ChannelMessageSend(msg.ChannelID, item.Format())
		}
	}
}
