package main

import (
	"os"
	"os/signal"

	log "github.com/Sirupsen/logrus"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	bot := New()

	bot.Open()
	defer bot.Close()

	log.RegisterExitHandler(func() { bot.Close() })

	// Listen for SIGINT and gracefully disconnect.
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt)

	<-signalChannel
}
