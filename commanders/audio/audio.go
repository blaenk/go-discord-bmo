package audio

import (
	"strings"

	"github.com/blaenk/bmo/bot"
	"github.com/bwmarrin/discordgo"
)

// Audio is an empty type that implements Commander.
type Audio struct{}

// New creates a new Audio instance.
func New() *Audio {
	return &Audio{}
}

// Command responds to the audio command.
func (a *Audio) Command(b *bot.Bot, msg *discordgo.Message) {
	b.EmbedLog().Infof("Message: %+v\n", msg)

	if b.MessageCommandsBot(msg) {
		invoker := msg.Author.ID
		command := b.MessageCommand(msg)

		if strings.HasPrefix(command, "pause") {
			b.Audio().Pause()
		}

		if strings.HasPrefix(command, "resume") {
			b.Audio().Resume()
		}

		if strings.HasPrefix(command, "skip") {
			b.Audio().Skip()
		}

		if strings.HasPrefix(command, "clear") {
			b.Audio().Clear()
		}

		// TODO
		// Support 'from' and 'to'
		// On a ticker interval, update file read Seek(0) progress to edit ASCII
		// progress bar of position in file
		// https://stackoverflow.com/questions/10901351/fgetpos-available-in-go-want-to-find-file-position
		if strings.HasPrefix(command, "play ") {
			target := command[5:]

			if target == "" {
				_, _ = b.ReplyToMessage(msg, "You didn't provide a URL!")
				return
			}

			channel, _ := b.Session().Channel(msg.ChannelID)

			voiceState, err := b.UserVoiceState(channel.GuildID, invoker)

			if err != nil {
				_, _ = b.ReplyToMessage(msg, "You're not in a voice channel!")
				return
			}

			// Get metadata and notify channel
			meta, err := bot.GetAudioMetadata(target)

			if err != nil {
				_, _ = b.ReplyToMessage(msg, "Couldn't resolve an audio URL :(")
				return
			}

			_, _ = b.Session().ChannelMessageSend(msg.ChannelID, "Queuing **"+meta.Title+"**")

			// FIXME
			// This blocks; do it in a separate goroutine?
			// TODO
			// Would be nice to be able to register OnProgress handlers for the ffmpeg
			// process and/or download progress
			convertedAudio, err := b.Audio().GetOrConvertFile(meta.AudioURL, meta.Origin)

			if err != nil {
				return
			}

			b.Audio().EnqueueAudioFile(voiceState.GuildID, voiceState.ChannelID, convertedAudio)
		}
	}
}
