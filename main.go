package main

import (
	"os"
	"os/signal"

	log "github.com/Sirupsen/logrus"
	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

type Ping struct{}

func (p *Ping) Command(bot *Bot, msg *discordgo.Message) {
	if msg.Content == "ping" {
		_, _ = bot.session.ChannelMessageSend(msg.ChannelID, "Pong!")
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	bot := New()

	bot.registerCommand(&Ping{})
	bot.registerPreviewer(&HackerNews{})

	bot.Open()
	defer bot.Close()

	log.RegisterExitHandler(func() { bot.Close() })

	// Listen for SIGINT and gracefully disconnect.
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt)

	<-signalChannel
}
