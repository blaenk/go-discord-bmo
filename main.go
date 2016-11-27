package main

import (
	"github.com/joho/godotenv"

	"log"
	"os"
	"os/signal"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	bot := New()

	bot.Open()
	defer bot.Close()

	// Listen for SIGINT and gracefully disconnect.
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt)

	<-signalChannel
}
