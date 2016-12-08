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

// AudioEvent is a self-contained representation of an intent to emit audio in a
// given voice channel.
type AudioEvent struct {
	guildID        string
	voiceChannelID string
	audioFile      string
}

// Audio contains the state needed for audio receiving and sending.
type Audio struct {
	userSSRCs          map[string]uint32
	streamDecoders     map[uint32]*gopus.Decoder
	opusEncoder        *gopus.Encoder
	sendingPCM         bool
	receivingPCM       bool
	lock               sync.Mutex
	discordAudioOutput chan []int16
}

// NewAudio creates an Audio struct
func NewAudio() *Audio {
	return &Audio{
		userSSRCs:          map[string]uint32{},
		streamDecoders:     map[uint32]*gopus.Decoder{},
		discordAudioOutput: make(chan []int16, channels),
	}
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

// Receive audio packets from the Discord voice connection and Opus-decode them
// into PCM.
func (a *Audio) receivePCM(voiceConnection *discordgo.VoiceConnection, outChannel chan<- *discordgo.Packet) {
	a.lock.Lock()

	if a.receivingPCM || outChannel == nil {
		a.lock.Unlock()
		return
	}

	a.receivingPCM = true
	a.lock.Unlock()

	defer func() { a.sendingPCM = false }()
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
		outChannel <- inboundAudioPacket
	}
}

// SendPCM sends s16le PCM from the given reader to the given voice connection.
func (a *Audio) SendPCM(voiceConnection *discordgo.VoiceConnection, reader io.Reader) {
	a.lock.Lock()
	defer a.lock.Unlock()

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
	for {
		// Obtain an audio frame for each channel from the ffmpeg process.
		pcmFrame := make([]int16, frameSize*channels)

		err = binary.Read(reader, binary.LittleEndian, &pcmFrame)

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
func (a *Audio) runFFMPEG(voiceConnection *discordgo.VoiceConnection, filePath string) {
	log.WithField("path", filePath).Info("Invoking FFMPEG")

	ffmpeg := exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-f", "s16le",
		"-ar", strconv.Itoa(frequency),
		"-ac", strconv.Itoa(channels),
		"pipe:1")

	ffmpegOut, err := ffmpeg.StdoutPipe()
	defer ffmpegOut.Close()

	if err != nil {
		log.WithError(err).Error("Couldn't obtain ffmpeg stdout")
		return
	}

	ffmpegBuffer := bufio.NewReaderSize(ffmpegOut, 16384)

	err = ffmpeg.Start()
	log.Info("Started FFMPEG")

	if err != nil {
		log.WithError(err).Error("Couldn't start ffmpeg")
		return
	}

	a.SendPCM(voiceConnection, ffmpegBuffer)
}
