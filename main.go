package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/joho/godotenv"

	"github.com/blaenk/bmo/bot"
	"github.com/blaenk/bmo/commanders/audio"
	"github.com/blaenk/bmo/commanders/ping"
	"github.com/blaenk/bmo/previewers/hn"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	log.SetFormatter(&log.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
	})

	bot := bot.New()

	bot.RegisterCommand(ping.New())
	bot.RegisterCommand(audio.New())

	bot.RegisterPreviewer(hn.New())

	bot.Open()
	defer bot.Close()

	log.RegisterExitHandler(func() { bot.Close() })

	signalChannel := make(chan os.Signal, 1)

	// Listen for SIGINT and gracefully disconnect.
	signal.Notify(signalChannel, os.Interrupt)

	// Listen for SIGPIPE for when e.g. we're using tee to print and save log
	// output.
	signal.Notify(signalChannel, syscall.SIGPIPE)

	<-signalChannel
}
