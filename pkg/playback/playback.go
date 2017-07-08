package playback

import (
	"encoding/json"
	"log"

	api "github.com/juanvallejo/streaming-server/pkg/api/types"
	"github.com/juanvallejo/streaming-server/pkg/socket/client"
	"github.com/juanvallejo/streaming-server/pkg/stream"
)

// StreamPlayback represents playback status for a given
// stream - there are one or more StreamPlayback instances
// for every one stream
type StreamPlayback struct {
	id        string
	queue     PlaybackQueue
	stream    stream.Stream
	startedBy string
	timer     *Timer
}

// UpdateStartedBy receives a client and updates the
// startedBy field with the client's current username
func (p *StreamPlayback) UpdateStartedBy(c *client.Client) {
	if name, hasName := c.GetUsername(); hasName {
		p.startedBy = name
		return
	}

	log.Printf("WRN SOCKET CLIENT PLAYBACK attempted to update `startedBy` information, but the current client with id %q has no registered username.", c.GetId())
}

// RefreshInfoFromClient receives a client and updates altered
// client details used as part of playback info.
// Returns a bool (true) if the client received contains
// old info matching the one stored by the playback handler,
// and such info has been since updated in the client.
func (p *StreamPlayback) RefreshInfoFromClient(c *client.Client) bool {
	cOldUser, hasOldUser := c.GetPreviousUsername()
	if !hasOldUser {
		return false
	}

	if len(p.startedBy) > 0 && p.startedBy == cOldUser {
		cUser, hasUser := c.GetUsername()
		if !hasUser {
			// should never happen, if a client has an "old username"
			// they MUST have a currently active username
			panic("client had previous username without an active / current username")
		}
		p.startedBy = cUser
		return true
	}

	return false
}

func (p *StreamPlayback) Pause() error {
	return p.timer.Pause()
}

func (p *StreamPlayback) Play() error {
	return p.timer.Play()
}

func (p *StreamPlayback) Stop() error {
	return p.timer.Stop()
}

func (p *StreamPlayback) Reset() error {
	return p.timer.Set(0)
}

func (p *StreamPlayback) SetTime(newTime int) error {
	p.timer.Set(newTime)
	return nil
}

func (p *StreamPlayback) GetTime() int {
	return p.timer.GetTime()
}

// OnTick calls the playback object's timer object and sets its
// "tick" callback function; called every tick increment interval.
func (p *StreamPlayback) OnTick(callback TimerCallback) {
	p.timer.OnTick(callback)
}

// QueueStreamUrl receives a stream url and pushes a loaded stream.Stream
// to the end of the playback queue for a given userId.
func (p *StreamPlayback) QueueStreamFromUrl(url string, user *client.Client, streamHandler stream.StreamHandler) error {
	s, err := p.GetOrCreateStreamFromUrl(url, streamHandler)
	if err != nil {
		return err
	}

	p.queue.Push(user.GetId(), s)
	return nil
}

func (p *StreamPlayback) GetQueue() PlaybackQueue {
	return p.queue
}

// GetStream returns a stream.Stream object containing current stream data
// tied to the current StreamPlayback object, or a bool (false) if there
// is no stream information currently loaded for the current StreamPlayback
func (p *StreamPlayback) GetStream() (stream.Stream, bool) {
	return p.stream, p.stream != nil
}

// SetStream receives a stream.Stream and sets it as the currently-playing stream
func (p *StreamPlayback) SetStream(s stream.Stream) {
	p.stream = s
}

// GetOrCreateStreamFromUrl receives a stream location (path, url, or unique identifier)
// and retrieves a corresponding stream.Stream, or creates a new one.
func (p *StreamPlayback) GetOrCreateStreamFromUrl(url string, streamHandler stream.StreamHandler) (stream.Stream, error) {
	if s, exists := streamHandler.GetStream(url); exists {
		log.Printf("INF PLAYBACK found existing stream object with url %q, retrieving...", url)
		return s, nil
	}

	s, err := streamHandler.NewStream(url)
	if err != nil {
		return nil, err
	}

	// if created new stream, fetch its duration info
	s.FetchMetadata(func(s stream.Stream, data []byte, err error) {
		if err != nil {
			log.Printf("ERR PLAYBACK FETCH-INFO-CALLBACK unable to calculate video metadata. Some information, such as media duration, will not be available: %v", err)
			return
		}

		err = s.SetInfo(data)
		if err != nil {
			log.Printf("ERR PLAYBACK FETCH-INFO-CALLBACK unable to set parsed stream info: %v", err)
			return
		}
	})

	log.Printf("INF PLAYBACK no stream found with url %q; creating... There are now %v registered streams", url, streamHandler.GetSize())
	return s, nil
}

// GetQueueStatus returns an ApiCodec describing the queue's current state
func (p *StreamPlayback) GetQueueStatus() api.ApiCodec {
	return p.queue.Status()
}

// GetQueueStackStatus returns an ApiCodec describing the queue's current state of the queue
// as well as the current state of the stack with the given stackId
func (p *StreamPlayback) GetQueueStackStatus(stackId string) (api.ApiCodec, error) {
	return p.queue.StackStatus(stackId)
}

// StreamPlaybackStatus is a serializable schema representing a summary of information
// about the current state of the StreamPlayback.
// Implements api.ApiCodec.
type StreamPlaybackStatus struct {
	Kind           string       `json:"kind"`
	QueueLength    int          `json:"queueLength"`
	StartedBy      string       `json:"startedBy"`
	StreamUrl      string       `json:"streamUrl"`
	StreamDuration float64      `json:"streamDuration"`
	TimerStatus    api.ApiCodec `json:"playback"`
}

func (s *StreamPlaybackStatus) Serialize() ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte{}, err
	}

	return b, nil
}

// Returns a map compatible with json types
// detailing the current playback status
func (p *StreamPlayback) GetStatus() api.ApiCodec {
	streamUrl := ""
	streamDuration := 0.0
	s, exists := p.GetStream()
	if exists {
		streamUrl = s.GetStreamURL()
		streamDuration = s.GetDuration()
	}

	return &StreamPlaybackStatus{
		Kind:           p.stream.GetKind(),
		QueueLength:    p.queue.Length(),
		StartedBy:      p.startedBy,
		StreamUrl:      streamUrl,
		StreamDuration: streamDuration,
		TimerStatus:    p.timer.Status(),
	}
}

func NewStreamPlayback(id string) *StreamPlayback {
	if len(id) == 0 {
		panic("A playback id is required to instantiate a new playback")
	}

	return &StreamPlayback{
		id:    id,
		timer: NewTimer(),
		queue: NewQueue(),
	}
}
