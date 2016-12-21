package bot

import (
	"bufio"
	"encoding/binary"
	"io"
	"os/exec"
	"strconv"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/bwmarrin/discordgo"
	"github.com/layeh/gopus"
)

// TODO
// Move audio event queue code here.
//
// Perhaps have QueueAudio() and ImmediateAudio(), where the latter pauses
// playback of the former before resuming.

const (
	channels         int = 2
	frequency        int = 48000
	frameSize        int = 960
	maxOpusFrameSize int = (frameSize * 2) * 2
)

const (
	playerActionReady = iota
	playerActionSkip
	playerActionAbort
	playerActionPause
	playerActionPreempt
)

type AudioBuffer interface {
	io.Reader
}

// AudioEvent is a self-contained representation of an intent to emit audio in a
// given voice channel.
type AudioEvent struct {
	guildID        string
	voiceChannelID string
	audio          io.Reader
}

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

	q.queue = make([]*AudioEvent, 10)
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
	defer q.cond.L.Unlock()

	if len(q.queue) < 2 {
		return
	}

	head, next := q.queue[0], q.queue[1]

	q.queue[0] = next
	q.queue[1] = head
}

func (q *AudioEventQueue) EnqueueFront(event *AudioEvent) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	q.queue = append([]*AudioEvent{event}, q.queue...)
}

func (q *AudioEventQueue) Dequeue() *AudioEvent {
	log.Info("Dequeueing")

	q.cond.L.Lock()

	log.Info("Acquired lock, checking len")

	for len(q.queue) == 0 {
		log.Info("Waiting on non-empty ...")
		q.cond.Wait()
	}

	log.Info("Queue no longer empty")

	defer func() {
		q.cond.Signal()
		q.cond.L.Unlock()
	}()

	log.Infof("Queue: %+v\n", q.queue)

	var event *AudioEvent
	event, q.queue = q.queue[0], q.queue[1:]
	return event
}

// Audio contains the state needed for audio receiving and sending.
type Audio struct {
	bot            *Bot
	userSSRCs      map[string]uint32
	streamDecoders map[uint32]*gopus.Decoder
	opusEncoder    *gopus.Encoder

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
	convertedAudio, err := a.convertFile(filePath)

	if err != nil {
		log.WithError(err).Error("Conversion error")
		return
	}

	log.Info("Converted audio file")

	a.queue.Enqueue(&AudioEvent{
		guildID:        guildID,
		voiceChannelID: voiceChannelID,
		audio:          convertedAudio,
	})

	log.Infof("Audio Queue: %+v\n", a.queue.queue)
}

func (a *Audio) ProcessAudioEventQueue() {
	a.bot.VoiceLog().Info("Starting PlayAudio goroutine")

	// TODO
	//
	// select on:
	//  * a.audioEvents for regular audio events
	//  * a.sideChannel for pause/resume, abort, defer

	// TODO
	// Lock a.playerState, use the condvar to wait on a.playerState != playerActionPause

	for i := 0; ; i++ {
		log.Info("Process frame", i)

		a.stateCond.L.Lock()

		for a.playerState == playerActionPause {
			log.Info("Waiting for unpause ...")
			a.stateCond.Wait()
		}

		log.Info("Resetting player state")
		a.playerState = playerActionReady

		a.stateCond.L.Unlock()

		// Process an audio event
		log.Info("Getting event")
		event := a.queue.Dequeue()
		log.Info("Got event")

		log.Infof("Event: %+v\n", event)

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
	log.Info("Waiting on send lock")

	a.sendCond.L.Lock()

	for a.sendingPCM {
		a.sendCond.Wait()
	}

	log.Info("Acquired send lock")
	a.sendingPCM = true

	defer func() {
		a.sendingPCM = false

		a.sendCond.Signal()
		a.sendCond.L.Unlock()

		log.Info("Released send lock")
	}()

	voiceConnection.Speaking(true)
	defer voiceConnection.Speaking(false)

	var err error

	a.opusEncoder, err = gopus.NewEncoder(frequency, channels, gopus.Audio)

	if err != nil {
		log.WithError(err).Error("Couldn't create an Opus Encoder")
		return
	}

	// TODO
	//
	// Allow preemption of audio.
	//
	// Perhaps use a `select` here that listens to an interrupt channel. When
	// interrupted, the buffer should be pushed back onto the event queue after
	// the preempting event is processed.
	//
	// In order to preempt this loop we'd need to save the work being done by
	// enqueuing it to the front of the queue before the preempting event is
	// itself enqueued to the front.
	//
	// To save our work we'd simply save the reader. This means we'd have two
	// types of AudioEvents:
	//
	// * regular file event (guildID, voiceChannelID, filePath)
	// * reader event (guildID, voiceChannelID, reader)
	//
	// Perhaps we can just store a Reader in the AudioEvent and then to a type
	// switch on it, so that if it's a File we will use f.Name() with runFFMPEG
	// and if it's just any other Reader then we use SendPCM.
	//
	// NOTE
	// Is it safe to just save the Reader if it's derived from an FFMPEG stdout?
	for {
		a.stateCond.L.Lock()

		switch a.playerState {
		case playerActionSkip:
			// Just quit the PCM-sending loop and let things here get garbage collected.
			a.stateCond.L.Unlock()
			// FIXME
			// In the case of an ffmpeg process backing this audio buffer we need to
			// kill it by doing ffmpeg.Process.Kill() and possibly also
			// ffmpeg.Process.Release() and possibly ffmpeg.Wait() which would
			// hopefully be immediate.
			return

		case playerActionPause:
			// Put the event back at the front of the queue and quit the PCM-sending loop.
			a.queue.EnqueueFront(event)
			a.stateCond.L.Unlock()
			return

		case playerActionAbort:
			// Clear the queue and quit the loop.
			a.queue.Clear()
			a.stateCond.L.Unlock()
			return

		case playerActionPreempt:
			a.queue.EnqueueFront(event)
			a.queue.Preempt()
			a.stateCond.L.Unlock()
			return
		}

		a.stateCond.L.Unlock()

		// Obtain an audio frame for each channel from the ffmpeg process.
		pcmFrame := make([]int16, frameSize*channels)

		err = binary.Read(event.audio, binary.LittleEndian, &pcmFrame)

		// FIXME
		// For these two cases we should dispose of the ffmpeg process in the case
		// of an ffmpeg-backed audio buffer, namely via ffmpeg.Wait()

		if err == io.EOF {
			log.Info("Reached EOF")
			return
		}

		if err == io.ErrUnexpectedEOF {
			log.Info("Reached Unexpected EOF")
			return
		}

		if err != nil {
			log.WithError(err).Error("Error reading from ffmpeg stdout")
			return
		}

		// Encode the PCM frame.
		opusFrame, err := a.opusEncoder.Encode(pcmFrame, frameSize, maxOpusFrameSize)

		if err != nil {
			log.WithError(err).Error("Encoding error")
			return
		}

		if !voiceConnection.Ready || voiceConnection.OpusSend == nil {
			log.Error("Client isn't ready to send Opus packets")
			return
		}

		// Send the Opus frame through the Discord voice connection.
		voiceConnection.OpusSend <- opusFrame
	}
}

// TODO
// Allow a Reader to be used as the audio source which is piped into ffmpeg
// through pipe:0.

// TODO
// Instead of piping out the audio, save it to a file? This would serve as a
// cache as well as being more stable, at the cost of having to wait until the
// entire conversion is complete.
func (a *Audio) convertFile(filePath string) (io.Reader, error) {
	log.WithField("path", filePath).Info("Invoking FFMPEG")

	ffmpeg := exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-f", "s16le",
		"-ar", strconv.Itoa(frequency),
		"-ac", strconv.Itoa(channels),
		"pipe:1")

	ffmpegOut, err := ffmpeg.StdoutPipe()
	// defer ffmpegOut.Close()

	if err != nil {
		log.WithError(err).Error("Couldn't obtain ffmpeg stdout")
		return nil, err
	}

	ffmpegBuffer := bufio.NewReaderSize(ffmpegOut, 16384)

	err = ffmpeg.Start()
	log.Info("Started FFMPEG")

	if err != nil {
		log.WithError(err).Error("Couldn't start ffmpeg")
		return nil, err
	}

	// FIXME
	// We should call ffmpeg.Wait() to wait until the process exits in order to
	// clear its associated resources, including the StdoutPipe. Not doing so
	// results in resource leaks.
	//
	// The problem is that then StdoutPipe will be closed so we can't read from
	// it. Some solutions to this include:
	//
	// * reading the entire buffer with ioutil.ReadAll/cmd.Output() -> []byte
	// * saving the result to a file and then reading from it later
	//
	// Both of these have downsides:
	//
	// * keeping the entire uncompressed PCM buffer in memory
	// * saving the entire uncompressed PCM buffer on disk
	//
	// It would be nice to use a Rust enum here to represent both
	// RegularBuffer(reader) and CommandBuffer(ffmpeg) and appropriately dispose
	// of each.
	//
	// Perhaps a middleground is to encode Opus to disk?

	return ffmpegBuffer, nil
}

// Receive audio packets from the Discord voice connection and Opus-decode them
// into PCM.
func (a *Audio) receivePCM(voiceConnection *discordgo.VoiceConnection) {
	// TODO
	//
	// use receiveCond

	// TODO
	//
	// When the voiceConnection is left this should be aborted.
	// Select on an interrupt channel?
	var err error

	for {
		if !voiceConnection.Ready || voiceConnection.OpusRecv == nil {
			log.Error("Client isn't ready to receive opus packets")
		}

		// Obtain an audio packet from Discord's audio input.
		inboundAudioPacket, ok := <-voiceConnection.OpusRecv

		if !ok {
			log.Info("No audio packet available")
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
				log.WithError(err).Error("Couldn't create Opus decoder")
				continue
			}

			a.streamDecoders[inboundAudioPacket.SSRC] = decoder
		}

		// Use the source stream-specific audio decoder to decode the Discord audio
		// packet into PCM.
		inboundAudioPacket.PCM, err = a.streamDecoders[inboundAudioPacket.SSRC].Decode(inboundAudioPacket.Opus, frameSize, false)

		if err != nil {
			log.WithError(err).Error("Couldn't decode Opus data")
			delete(a.streamDecoders, inboundAudioPacket.SSRC)
			continue
		}

		// Send the decoded PCM frame
		if a.OnInboundAudioPacket != nil {
			a.OnInboundAudioPacket(inboundAudioPacket)
		}
	}
}
