package bot

import "github.com/bwmarrin/discordgo"

// Commander represents a command responder
type Commander interface {
	Command(bot *Bot, message *discordgo.Message)
}
