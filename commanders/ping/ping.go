package ping

import (
	"github.com/blaenk/bmo/bot"
	"github.com/bwmarrin/discordgo"
)

// Ping is an empty type that implements Commander.
type Ping struct{}

// New creates a new Ping instance.
func New() *Ping {
	return &Ping{}
}

// Command responds to the ping command.
func (p *Ping) Command(bot *bot.Bot, msg *discordgo.Message) {
	if msg.Content == "ping" {
		_, _ = bot.Session().ChannelMessageSend(msg.ChannelID, "Pong!")
	}
}
