package bot

import (
	"net/url"

	"github.com/bwmarrin/discordgo"
)

// Previewer represents a type that is capable of previewing a given URL.
type Previewer interface {
	Preview(bot *Bot, message *discordgo.Message, url *url.URL)
}
