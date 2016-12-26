package bot

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/bwmarrin/discordgo"
	"github.com/layeh/gopus"
)

const (
	channels  int = 2
	frequency int = 48000
	frameSize int = 960
)

const (
	playerActionReady = iota
	playerActionSkip
	playerActionAbort
	playerActionPause
	playerActionPreempt
)

// AudioEvent is a self-contained representation of an intent to emit audio in a
// given voice channel.
type AudioEvent struct {
	guildID        string
	voiceChannelID string
	audio          io.ReadCloser
}

// AudioEventQueue represents a blocking audio event queue.
type AudioEventQueue struct {
	cond  *sync.Cond
	queue []*AudioEvent
}

func NewAudioEventQueue() *AudioEventQueue {
	return &AudioEventQueue{
		queue: make([]*AudioEvent, 0, 10),
		cond:  sync.NewCond(new(sync.Mutex)),
	}
}

func (q *AudioEventQueue) Clear() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for _, event := range q.queue {
		event.audio.Close()
	}

	q.queue = make([]*AudioEvent, 0, 10)
}

func (q *AudioEventQueue) Enqueue(event *AudioEvent) {
	q.cond.L.Lock()

	q.queue = append(q.queue, event)

	q.cond.Signal()
	q.cond.L.Unlock()
}

// Preempt swaps the head and the event after it.
func (q *AudioEventQueue) Preempt() {
	q.cond.L.Lock()

	if len(q.queue) < 2 {
		return
	}

	head, next := q.queue[0], q.queue[1]

	q.queue[0] = next
	q.queue[1] = head

	q.cond.L.Unlock()
}

func (q *AudioEventQueue) EnqueueFront(event *AudioEvent) {
	q.cond.L.Lock()

	q.queue = append([]*AudioEvent{event}, q.queue...)

	q.cond.L.Unlock()
}

func (q *AudioEventQueue) Dequeue() *AudioEvent {
	q.cond.L.Lock()

	for len(q.queue) == 0 {
		q.cond.Wait()
	}

	var event *AudioEvent
	event, q.queue = q.queue[0], q.queue[1:]

	q.cond.Signal()
	q.cond.L.Unlock()

	return event
}

// Audio contains the state needed for audio receiving and sending.
type Audio struct {
	bot            *Bot
	userSSRCs      map[string]uint32
	streamDecoders map[uint32]*gopus.Decoder

	sendingPCM   bool
	receivingPCM bool
	playerState  int

	sendCond    *sync.Cond
	receiveCond *sync.Cond
	stateCond   *sync.Cond

	discordAudioOutput   chan []int16
	OnInboundAudioPacket func(*discordgo.Packet)

	queue *AudioEventQueue
}

// NewAudio creates an Audio struct
func NewAudio(bot *Bot) *Audio {
	return &Audio{
		bot:                bot,
		sendCond:           sync.NewCond(new(sync.Mutex)),
		receiveCond:        sync.NewCond(new(sync.Mutex)),
		stateCond:          sync.NewCond(new(sync.Mutex)),
		userSSRCs:          map[string]uint32{},
		streamDecoders:     map[uint32]*gopus.Decoder{},
		discordAudioOutput: make(chan []int16, channels),
		queue:              NewAudioEventQueue(),
	}
}

func (a *Audio) Skip() {
	a.stateCond.L.Lock()

	a.playerState = playerActionSkip

	a.stateCond.Signal()
	a.stateCond.L.Unlock()
}

func (a *Audio) Abort() {
	a.stateCond.L.Lock()

	a.playerState = playerActionAbort

	a.stateCond.Signal()
	a.stateCond.L.Unlock()
}

func (a *Audio) Pause() {
	a.stateCond.L.Lock()

	a.playerState = playerActionPause

	a.stateCond.Signal()
	a.stateCond.L.Unlock()
}

func (a *Audio) Preempt(guildID, voiceChannelID, filePath string) {
	a.stateCond.L.Lock()
	defer func() {
		a.stateCond.Signal()
		a.stateCond.L.Unlock()
	}()

	a.playerState = playerActionPreempt

	a.EnqueueAudioFile(guildID, voiceChannelID, filePath)
}

func (a *Audio) EnqueueAudioFile(guildID, voiceChannelID, filePath string) {
	// TODO
	// This won't preserve enqueue order. e.g. if 1st enqueue takes 1hr to convert
	// but 2nd enqueue takes 10 seconds, 2nd enqueue will effectively be enqueued
	// first.
	//
	// Perhaps if the event is enqueued immediately but an Event.convert() is
	// potentially run in a goroutine, eventually fulfilling an os.File* on the
	// event struct when ready, while an attempt to read event.Audio() will block
	// until it's available?
	go func() {
		convertedAudio, err := a.getOrConvertFile(filePath)

		if err != nil {
			return
		}

		a.queue.Enqueue(&AudioEvent{
			guildID:        guildID,
			voiceChannelID: voiceChannelID,
			audio:          convertedAudio,
		})
	}()
}

func (a *Audio) ProcessAudioEventQueue() {
	a.bot.VoiceLog().Info("Starting PlayAudio goroutine")

	for {
		a.stateCond.L.Lock()

		for a.playerState == playerActionPause {
			a.stateCond.Wait()
		}

		a.playerState = playerActionReady

		a.stateCond.L.Unlock()

		// Process an audio event
		event := a.queue.Dequeue()

		a.bot.VoiceLog().WithFields(log.Fields{
			"guild":   event.guildID,
			"channel": event.voiceChannelID,
		}).Info("Received AudioEvent")

		// TODO
		// Wrap ChannelVoiceJoin so that it automatically logs?
		voiceConnection, err := a.bot.Session().ChannelVoiceJoin(event.guildID, event.voiceChannelID, false, true)

		if err != nil {
			a.bot.VoiceLog().WithFields(log.Fields{
				"guild":   event.guildID,
				"channel": event.voiceChannelID,
			}).WithError(err).Error("Couldn't join voice channel")

			continue
		}

		a.bot.VoiceLog().WithFields(log.Fields{
			"guild":   event.guildID,
			"channel": event.voiceChannelID,
		}).Info("Joined channel")

		// TODO
		// Will this repetitively add handlers?
		voiceConnection.AddHandler(a.onVoiceSpeakingUpdate)

		a.SendPCM(voiceConnection, event)

	}

	a.bot.VoiceLog().Fatal("Exited playAudio")
}

// TODO
//
// Need to ensure that the Decoder map doesn't leak resources by ensuring
// that we remove the Decoder whenever the source no longer exists.
//
// To do this we can establish a VoiceSpeakingUpdate handler for a given
// VoiceConnection which will maintain a map of UserID → SSRC, while a
// handler for VoiceStateUpdate will check for when a UserID leaves a voice
// channel without joining another one. When this happens, the UserID's SSRC
// should be looked up and the entry for that SSRC should be removed from
// the decoders map.
//
// Since that logic is specific to Audio, it would be preferable if this
// type simply exposed methods such as onVoiceSpeakingUpdate() and
// onVoiceStateUpdate() which are invoked by the Bot.

func (a *Audio) onVoiceSpeakingUpdate(voiceConnection *discordgo.VoiceConnection, speakingUpdate *discordgo.VoiceSpeakingUpdate) {
	// In discordgo VoiceSpeakingUpdate.SSRC is int while it's uint32 everywhere
	// else.
	a.userSSRCs[speakingUpdate.UserID] = uint32(speakingUpdate.SSRC)
}

func (a *Audio) onUserLeaveVoiceChannel(voiceState *discordgo.VoiceState) {
	// User left the channel, either their SSRC is no longer relevant or it may
	// change if they join another channel (?), so to be safe remove their entry.
	//
	// Note that this may be a critical section. For example, what if this
	// triggers while in the middle of receivePCM()? Should it multiplex on a
	// channel that receives such a notification? Or what if the user has left but
	// we still have audio buffered that we're in the process of decoding?
	if voiceState.ChannelID == "" {
		delete(a.streamDecoders, a.userSSRCs[voiceState.UserID])
		delete(a.userSSRCs, voiceState.UserID)
	}
}

// SendPCM sends s16le PCM from the given reader to the given voice connection.
func (a *Audio) SendPCM(voiceConnection *discordgo.VoiceConnection, event *AudioEvent) {
	a.sendCond.L.Lock()

	for a.sendingPCM {
		a.sendCond.Wait()
	}

	a.sendingPCM = true

	defer func() {
		a.sendingPCM = false

		a.sendCond.Signal()
		a.sendCond.L.Unlock()

		a.bot.VoiceLog().Info("Released send lock")
	}()

	voiceConnection.Speaking(true)

	for {
		a.stateCond.L.Lock()

		switch a.playerState {
		case playerActionSkip:
			// Just quit the PCM-sending loop and let things here get garbage collected.
			event.audio.Close()

			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)

			a.stateCond.L.Unlock()
			return

		case playerActionPause:
			// Put the event back at the front of the queue and quit the PCM-sending loop.
			a.queue.EnqueueFront(event)
			a.stateCond.L.Unlock()
			return

		case playerActionAbort:
			// Clear the queue and quit the loop.
			a.queue.Clear()
			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)

			a.stateCond.L.Unlock()
			return

		case playerActionPreempt:
			a.queue.EnqueueFront(event)
			a.queue.Preempt()

			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)

			a.stateCond.L.Unlock()
			return
		}

		a.stateCond.L.Unlock()

		// 128 [kb] * 20 [frame size] / 8 [byte] = 320
		opusFrame := make([]byte, 320)

		err := binary.Read(event.audio, binary.LittleEndian, &opusFrame)

		if err == io.EOF {
			a.bot.VoiceLog().Info("Audio EOF")
			event.audio.Close()

			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)
			return
		}

		if err == io.ErrUnexpectedEOF {
			a.bot.VoiceLog().Info("Audio unexpected EOF")
			event.audio.Close()

			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)
			return
		}

		if err != nil {
			a.bot.VoiceLog().WithError(err).Error("Error reading from ffmpeg stdout")
			event.audio.Close()

			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)
			return
		}

		if !voiceConnection.Ready || voiceConnection.OpusSend == nil {
			a.bot.VoiceLog().Error("Client isn't ready to send Opus packets")
			// Keep looping until it's ready, otherwise this event will be dropped.

			time.Sleep(250 * time.Millisecond)
			voiceConnection.Speaking(false)
			return
		}

		// Send the Opus frame through the Discord voice connection.
		voiceConnection.OpusSend <- opusFrame
	}
}

func (a *Audio) getOrConvertFile(filePath string) (io.ReadCloser, error) {
	a.bot.VoiceLog().WithField("path", filePath).Info("Getting or converting file")

	shaSum := fmt.Sprintf("%x", sha1.Sum([]byte(filePath)))
	audioPath := path.Join("./data/opus", shaSum)

	if _, err := os.Stat(audioPath); err == nil {
		a.bot.VoiceLog().WithField("path", audioPath).Info("Cache Hit: Opus audio")
		return os.Open(audioPath)
	}

	a.bot.VoiceLog().WithField("path", filePath).Info("Invoking FFMPEG")

	ffmpeg := exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-f", "data",
		"-map", "0:a",
		"-ar", strconv.Itoa(frequency),
		"-ac", strconv.Itoa(channels),
		"-acodec", "libopus",
		"-sample_fmt", "s16",
		"-vbr", "off",
		"-b:a", "128000",
		"-compression_level", "10",
		audioPath)

	err := ffmpeg.Start()

	a.bot.VoiceLog().Info("FFMPEG started")

	if err != nil {
		a.bot.VoiceLog().WithError(err).Error("Couldn't start ffmpeg")
		return nil, err
	}

	err = ffmpeg.Wait()

	a.bot.VoiceLog().Info("FFMPEG finished")

	if err != nil {
		a.bot.VoiceLog().WithError(err).Error("Conversion error")
		return nil, err
	}

	a.bot.VoiceLog().WithFields(log.Fields{
		"from": filePath,
		"to":   audioPath,
	}).Info("Encoded Opus")

	return os.Open(audioPath)
}

// Receive audio packets from the Discord voice connection and Opus-decode them
// into PCM.
func (a *Audio) receivePCM(voiceConnection *discordgo.VoiceConnection) {
	// TODO
	// Use receiveCond.

	// TODO
	//
	// When the voiceConnection is left this should be aborted.
	// Select on an interrupt channel?
	var err error

	for {
		if !voiceConnection.Ready || voiceConnection.OpusRecv == nil {
			a.bot.VoiceLog().Error("Client isn't ready to receive opus packets")
		}

		// Obtain an audio packet from Discord's audio input.
		inboundAudioPacket, ok := <-voiceConnection.OpusRecv

		if !ok {
			a.bot.VoiceLog().Info("No audio packet available")
			return
		}

		// An SSRC is a synchronization source identifier that uniquely identifies
		// the source of a stream. This probably means that there will be a separate
		// SSRC for each person transmitting audio which we are receiving.
		//
		// For this reason we create a separate Opus decoder for each source stream
		// to avoid mixing up their internal states on separate streams.
		if _, ok = a.streamDecoders[inboundAudioPacket.SSRC]; !ok {
			decoder, err := gopus.NewDecoder(frequency, channels)

			if err != nil {
				a.bot.VoiceLog().WithError(err).Error("Couldn't create Opus decoder")
				continue
			}

			a.streamDecoders[inboundAudioPacket.SSRC] = decoder
		}

		// Use the source stream-specific audio decoder to decode the Discord audio
		// packet into PCM.
		inboundAudioPacket.PCM, err = a.streamDecoders[inboundAudioPacket.SSRC].Decode(inboundAudioPacket.Opus, frameSize, false)

		if err != nil {
			a.bot.VoiceLog().WithError(err).Error("Couldn't decode Opus data")
			delete(a.streamDecoders, inboundAudioPacket.SSRC)
			continue
		}

		// Send the decoded PCM frame
		if a.OnInboundAudioPacket != nil {
			a.OnInboundAudioPacket(inboundAudioPacket)
		}
	}
}
