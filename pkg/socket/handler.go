package socket

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/juanvallejo/streaming-server/pkg/playback"
	"github.com/juanvallejo/streaming-server/pkg/playback/queue"
	playbackutil "github.com/juanvallejo/streaming-server/pkg/playback/util"
	"github.com/juanvallejo/streaming-server/pkg/socket/client"
	"github.com/juanvallejo/streaming-server/pkg/socket/cmd"
	"github.com/juanvallejo/streaming-server/pkg/socket/connection"
	socketserver "github.com/juanvallejo/streaming-server/pkg/socket/server"
	"github.com/juanvallejo/streaming-server/pkg/socket/util"
	"github.com/juanvallejo/streaming-server/pkg/stream"
)

type Handler struct {
	clientHandler   client.SocketClientHandler
	CommandHandler  cmd.SocketCommandHandler
	PlaybackHandler playback.PlaybackHandler
	StreamHandler   stream.StreamHandler

	server *socketserver.Server
}

const (
	ROOM_DEFAULT_STREAMSYNC_RATE         = 10 // seconds to wait before emitting streamsync to clients
	ROOM_DEFAULT_STREAMSYNC_LOGGING_RATE = 50
)

func (h *Handler) HandleClientConnection(conn connection.Connection) {
	log.Printf("INF SOCKET CONN client (%s) has connected with id %q\n", conn.Request().RemoteAddr, conn.UUID())

	h.RegisterClient(conn)
	log.Printf("INF SOCKET currently %v clients registered\n", h.clientHandler.GetClientSize())

	conn.On("disconnection", func(data connection.MessageDataCodec) {
		log.Printf("INF DCONN SOCKET client with id %q has disconnected\n", conn.UUID())

		if c, err := h.clientHandler.GetClient(conn.UUID()); err == nil {
			userName, exists := c.GetUsername()
			if !exists {
				userName = c.UUID()
			}
			c.BroadcastFrom("info_clientleft", &client.Response{
				Id:   conn.UUID(),
				From: userName,
			})

			ns, exists := c.Namespace()
			if exists {
				sPlayback, sPlaybackExists := h.PlaybackHandler.PlaybackByNamespace(ns)
				if sPlaybackExists {
					// update room's last updated time to give buffer
					// between last client leaving and room reaping.
					sPlayback.SetLastUpdated(time.Now())
				}

				// remove user from authorizer role-bindings
				authorizer := h.CommandHandler.Authorizer()
				sPlayback.HandleDisconnection(c.Connection(), authorizer, h.clientHandler)
				if authorizer != nil {
					for _, b := range authorizer.Bindings() {
						b.RemoveSubject(c.Connection())
					}
				}
			}
		}

		if err := h.DeregisterClient(conn); err != nil {
			log.Printf("ERR SOCKET %v", err)
		}
	})

	// this event is received when a client is requesting a username update
	conn.On("request_updateusername", func(data connection.MessageDataCodec) {
		messageData, ok := data.(connection.MessageData)
		if !ok {
			log.Printf("ERR SOCKET CLIENT socket connection event handler for event %q received data of wrong type. Expecting connection.MessageData", "request_chatmessage")
			return
		}

		rawUsername, ok := messageData.Key("user")
		if !ok {
			log.Printf("ERR SOCKET CLIENT client %q sent malformed request to update username. Ignoring request.", conn.UUID())
			return
		}

		username, ok := rawUsername.(string)
		if !ok {
			log.Printf("ERR SOCKET CLIENT client %q sent a non-string value for the field %q", conn.UUID(), "username")
			return
		}

		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT %v. Broadcasting as info_clienterror event", err)
			c.BroadcastErrorTo(err)
			return
		}

		err = util.UpdateClientUsername(c, username, h.clientHandler)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT %v. Broadcasting as \"info_clienterror\" event", err)
			c.BroadcastErrorTo(err)
			return
		}
	})

	// this event is received when a client is requesting to broadcast a chat message
	conn.On("request_chatmessage", func(data connection.MessageDataCodec) {
		messageData, ok := data.(connection.MessageData)
		if !ok {
			log.Printf("ERR SOCKET CLIENT socket connection event handler for event %q received data of wrong type. Expecting connection.MessageData", "request_chatmessage")
			return
		}

		username, ok := messageData.Key("user")
		if ok {
			log.Printf("INF SOCKET CLIENT client with id %q requested a chat message broadcast with name %q", conn.UUID(), username)
		}

		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT could not retrieve client. Ignoring request_chatmessage request: %v", err)
			return
		}

		command, isCommand, err := h.ParseCommandMessage(c, messageData)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to parse client chat message as command: %v", err)
			c.BroadcastSystemMessageTo(err.Error())
			return
		}

		if isCommand {
			cmdSegments := strings.Split(command, " ")
			cmdArgs := []string{}
			if len(cmdSegments) > 1 {
				cmdArgs = cmdSegments[1:]
			}

			log.Printf("INF SOCKET CLIENT interpreting chat message as user command %q for client id (%q) with name %q", command, conn.UUID(), username)
			result, err := h.CommandHandler.ExecuteCommand(cmdSegments[0], cmdArgs, c, h.clientHandler, h.PlaybackHandler, h.StreamHandler)
			if err != nil {
				log.Printf("ERR SOCKET CLIENT unable to execute command with id %q: %v", command, err)
				c.BroadcastSystemMessageTo(err.Error())
				return
			}

			if len(result) > 0 {
				c.BroadcastSystemMessageTo(result)
			}
			return
		}

		images, err := h.ParseMessageMedia(messageData)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to parse client chat message media: %v", err)
			return
		}

		res := &client.Response{
			Id:    c.UUID(),
			From:  "system",
			Extra: make(map[string]interface{}),
		}

		// if images could be extracted from message, add to response
		if len(images) > 0 {
			res.Extra["images"] = images
		}

		b, err := data.Serialize()
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to serialize client chat message data: %v", err)
			return
		}

		err = json.Unmarshal(b, res)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to de-serialize client chat message into client response: %v", err)
			return
		}

		c.BroadcastAll("chatmessage", res)
		fmt.Printf("INF SOCKET CLIENT chatmessage received %v\n", data)
	})

	// this event is received when a client is requesting authorization endpoint information
	conn.On("request_authorization", func(data connection.MessageDataCodec) {
		log.Printf("INF SOCKET CLIENT AUTHZ client with id %q requested authorization information", conn.UUID())

		// send an httprequest event to the client with authz endpoint information
		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to retrieve client from connection id. Ignoring request_streamsync request: %v", err)
			return
		}

		c.BroadcastAuthRequestTo("init")
	})

	// this event is received when a client is requesting the current queue state
	conn.On("request_queuesync", func(data connection.MessageDataCodec) {
		log.Printf("INF SOCKET CLIENT client with id %q requested a queue-sync", conn.UUID())

		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to retrieve client from connection id. Ignoring request_streamsync request: %v", err)
			return
		}

		sPlayback, err := h.getPlaybackFromClient(c)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT %v", err)
			c.BroadcastErrorTo(err)
			return
		}

		res := &client.Response{
			Id:   c.UUID(),
			From: "system",
		}

		b, err := sPlayback.GetQueue().Serialize()
		if err != nil {
			return
		}

		err = json.Unmarshal(b, &res.Extra)
		if err != nil {
			return
		}

		c.BroadcastTo("queuesync", res)
	})

	// this event is received when a client is requesting the current queue state for a specific Queue stack
	conn.On("request_stacksync", func(data connection.MessageDataCodec) {
		log.Printf("INF SOCKET CLIENT client with id %q requested a queue-stack-sync", conn.UUID())

		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to retrieve client from connection id. Ignoring request_streamsync request: %v", err)
			return
		}

		sPlayback, err := h.getPlaybackFromClient(c)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT %v", err)
			c.BroadcastErrorTo(err)
			return
		}

		res := &client.Response{
			Id:   c.UUID(),
			From: "system",
		}

		userQueue, exists, err := playbackutil.GetUserQueue(c, sPlayback.GetQueue())
		if err != nil {
			return
		}
		if !exists {
			userQueue = queue.NewAggregatableQueue(c.UUID())
		}

		b, err := userQueue.Serialize()
		if err != nil {
			return
		}

		err = json.Unmarshal(b, &res.Extra)
		if err != nil {
			return
		}

		c.BroadcastTo("stacksync", res)
	})

	// this event is received when a client is requesting current stream state information
	conn.On("request_streamsync", func(data connection.MessageDataCodec) {
		log.Printf("INF SOCKET CLIENT client with id %q requested a streamsync", conn.UUID())

		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to retrieve client from connection id. Ignoring request_streamsync request: %v", err)
			return
		}

		sPlayback, err := h.getPlaybackFromClient(c)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT %v", err)
			c.BroadcastErrorTo(err)
			return
		}

		res := &client.Response{
			Id: c.UUID(),
		}

		err = util.SerializeIntoResponse(sPlayback.GetStatus(), &res.Extra)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to serialize playback status: %v", err)
			return
		}

		c.BroadcastTo("streamsync", res)
	})

	// this event is received when a client is requesting current stream user information
	conn.On("request_userlist", func(data connection.MessageDataCodec) {
		log.Printf("INF SOCKET CLIENT client with id %q requested a userlist", conn.UUID())

		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to retrieve user info for connection id %q. No such user associated with id.", conn.UUID())
			return
		}

		ns, exists := c.Namespace()
		if !exists {
			log.Printf("ERR SOCKET CLIENT client with id %q requested a user list for room, but client is not currently in a room. Broadcasting error...", conn.UUID())
			c.BroadcastErrorTo(fmt.Errorf("error: unable to get user list - you are not currently in a room"))
			return
		}

		userList := &client.SerializableClientList{}
		for _, conn := range c.Connections() {
			user, err := h.clientHandler.GetClient(conn.UUID())
			if err != nil {
				continue
			}

			roles := []string{}
			authorizer := h.CommandHandler.Authorizer()
			if authorizer != nil {
				for _, b := range authorizer.Bindings() {
					for _, u := range b.Subjects() {
						if u.UUID() == conn.UUID() {
							roles = append(roles, b.Role().Name())
						}
					}
				}
			}

			username, _ := user.GetUsername()
			userList.Clients = append(userList.Clients, client.SerializableClient{
				Username: username,
				Id:       user.UUID(),
				Room:     ns.Name(),
				Roles:    roles,
			})
		}

		c.BroadcastTo("userlist", userList)
	})

	// this event is received when a client is requesting to update stream state information in the server
	conn.On("streamdata", func(data connection.MessageDataCodec) {
		c, err := h.clientHandler.GetClient(conn.UUID())
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to retrieve client from connection id. Ignoring request_streamsync request: %v", err)
			return
		}

		ns, exists := c.Namespace()
		if !exists {
			log.Printf("ERR SOCKET CLIENT client with id (%q) has no room association. Ignoring streamsync request.", c.UUID())
			return
		}

		sPlayback, exists := h.PlaybackHandler.PlaybackByNamespace(ns)
		if !exists {
			log.Printf("ERR SOCKET CLIENT client with id (%q) requested a streamsync but no Playback could be found associated with that client.", c.UUID())
			c.BroadcastErrorTo(fmt.Errorf("Warning: could not update stream playback. No room could be detected."))
			return
		}

		s, exists := sPlayback.GetStream()
		if !exists {
			log.Printf("ERR SOCKET CLIENT client with id (%q) sent updated streamdata but no stream could be found associated with the current playback.", c.UUID())
			return
		}

		jsonData, err := data.Serialize()
		if err != nil {
			log.Printf("ERR SOCKET CLIENT unable to convert received data map into json string: %v", err)
		}

		log.Printf("INF SOCKET CLIENT received streaminfo from client with id (%q). Updating stream information...", c.UUID())
		err = s.SetInfo(jsonData)
		if err != nil {
			log.Printf("ERR SOCKET CLIENT error updating stream data: %v", err)
			return
		}
	})
}

// ParseMessageMedia receives connection.MessageData and parses
// image urls in the "message" key, removing urls from the
// text message, and returning them as a slice of strings
func (h *Handler) ParseMessageMedia(data connection.MessageData) ([]string, error) {
	message, ok := data.Key("message")
	if !ok {
		return []string{}, fmt.Errorf("error: invalid client message format; message field empty")
	}

	rawText, ok := message.(string)
	if !ok {
		return []string{}, fmt.Errorf("error: client message media parse error; unable to cast message to string")
	}

	re := regexp.MustCompile("(http(s)?://[^ ]+\\.(jpg|png|gif|jpeg))( )?")
	urls := re.FindAllString(rawText, -1)
	if urls == nil || len(urls) == 0 {
		return []string{}, nil
	}

	newText := re.ReplaceAllString(rawText, "")
	data.Set("message", newText)

	return urls, nil
}

// ParseCommandMessage receives a client pointer and a data map sent by a client
// and determines whether the "message" field in the client data map contains a
// valid client command. An error is returned if there are any errors while parsing
// the message field. A boolean (true) is returned if the message field contains a
// valid client command, along with a string ("command") containing a StreamCommand id
//
// A valid client command will always begin with a "/" and never contain more than
// one "/" character.
func (h *Handler) ParseCommandMessage(client *client.Client, data connection.MessageData) (string, bool, error) {
	message, ok := data.Key("message")
	if !ok {
		return "", false, fmt.Errorf("error: invalid client command format; message field empty")
	}

	command, ok := message.(string)
	if !ok {
		return "", false, fmt.Errorf("error: client command parse error; unable to cast message to string")
	}

	if string(command[0]) != "/" {
		return "", false, nil
	}

	return command[1:], true, nil
}

// RegisterClient receives a socket connection, creates a new client, and assigns the client to a room.
// if client is first to join room, then the room did not exist before; if this is the case, a new
// streamPlayback object is created to represent the "room" in memory. The streamPlayback's id becomes
// the client's room name.
// If a streamPlayback already exists for the current "room" and the streamPlayback has a reference to a
// stream.Stream, a "streamload" event is sent to the client with the current stream.Stream information.
// This method is not concurrency-safe.
func (h *Handler) RegisterClient(conn connection.Connection) {
	log.Printf("INF SOCKET CLIENT registering client with id %q\n", conn.UUID())

	c := h.clientHandler.CreateClient(conn)
	c.BroadcastFrom("info_clientjoined", &client.Response{
		Id: c.UUID(),
	})

	namespace, nsExists := c.Namespace()
	if !nsExists {
		log.Printf("INF SOCKET SERVER client registration error: invalid or unknown namespace for connection with id (%s)", conn.UUID())
		return
	}

	if len(namespace.Name()) == 0 {
		log.Printf("INF SOCKET SERVER client namespace registration error: empty namespace name provided for connection with id (%s)\n", conn.UUID())
		return
	}

	// TODO: use a handler to broadcast to namespace

	sPlayback, exists := h.PlaybackHandler.PlaybackByNamespace(namespace)
	if !exists {
		log.Printf("INF SOCKET CLIENT Playback did not exist for room with namespace %v. Creating...", namespace)
		sPlayback = h.PlaybackHandler.NewPlayback(namespace, h.CommandHandler.Authorizer(), h.clientHandler)
		sPlayback.OnTick(func(currentTime int) {
			currPlayback, exists := h.PlaybackHandler.PlaybackByNamespace(namespace)
			if !exists {
				log.Printf("ERR CALLBACK-PLAYBACK SOCKET CLIENT attempted to send streamsync event to client, but stream playback does not exist.")
				return
			}

			if currentTime%2 == 0 {
				currStream, streamExists := currPlayback.GetStream()
				if streamExists {
					// if stream exists and playback timer >= playback stream duration, stop stream
					// or queue the next item in the playback queue (if queue not empty)
					if currStream.GetDuration() > 0 && float64(currPlayback.GetTime()) >= currStream.GetDuration() {
						queue := currPlayback.GetQueue()
						queueItem, err := queue.Next()
						if err == nil {
							log.Printf("INF CALLBACK-PLAYBACK SOCKET CLIENT detected end of stream. Auto-queuing next stream...")

							nextStream, ok := queueItem.(stream.Stream)
							if !ok {
								log.Printf("ERR CALLBACK-PLAYBACK SOCKET CLIENT expected next queue item to implement stream.Stream... Unable to advance the queue.")
								return
							}

							currPlayback.SetStream(nextStream)
							currPlayback.Reset()

							res := &client.Response{
								Id:   c.UUID(),
								From: "system",
							}

							err = util.SerializeIntoResponse(currPlayback.GetStatus(), &res.Extra)
							if err != nil {
								log.Printf("ERR CALLBACK-PLAYBACK SOCKET CLIENT unable to serialize nextStream codec: %v", err)
								return
							}

							c.BroadcastAll("streamload", res)
						} else {
							log.Printf("INF CALLBACK-PLAYBACK SOCKET CLIENT detected end of stream and no queue items. Stopping stream...")
							currPlayback.Stop()
						}

						// emit updated playback state to client if stream has ended
						log.Printf("INF CALLBACK-PLAYBACK SOCKET CLIENT stream has ended after %v seconds.", currentTime)
						res := &client.Response{
							Id: c.UUID(),
						}

						err = util.SerializeIntoResponse(currPlayback.GetStatus(), &res.Extra)
						if err != nil {
							log.Printf("ERR CALLBACK-PLAYBACK SOCKET CLIENT unable to serialize playback status: %v", err)
							return
						}

						c.BroadcastAll("streamsync", res)
					}
				}
			}

			// if stream timer has not reached its duration, wait until next ROOM_DEFAULT_STREAMSYNC_RATE tick
			// before updating client with playback information
			if currentTime%ROOM_DEFAULT_STREAMSYNC_RATE != 0 {
				return
			}

			// log in 50 second intervals
			if currentTime%ROOM_DEFAULT_STREAMSYNC_LOGGING_RATE == 0 {
				log.Printf("INF CALLBACK-PLAYBACK SOCKET CLIENT streamsync event sent after %v seconds", currentTime)
			}

			res := &client.Response{
				Id: c.UUID(),
			}

			err := util.SerializeIntoResponse(currPlayback.GetStatus(), &res.Extra)
			if err != nil {
				log.Printf("ERR CALLBACK-PLAYBACK SOCKET CLIENT unable to serialize playback status: %v", err)
				return
			}

			c.BroadcastAll("streamsync", res)
		})

		return
	}

	sPlayback.SetLastUpdated(time.Now())

	log.Printf("INF SOCKET CLIENT found Playback for room with name %q", namespace.Name())

	pStream, exists := sPlayback.GetStream()
	if exists {
		log.Printf("INF SOCKET CLIENT found stream info (%s) associated with Playback for room with name %q... Sending \"streamload\" signal to client", pStream.GetStreamURL(), namespace)
		res := &client.Response{
			Id: c.UUID(),
		}

		err := util.SerializeIntoResponse(sPlayback.GetStatus(), &res.Extra)
		if err != nil {
			log.Printf("ERR CALLBACK-PLAYBACK SOCKET CLIENT unable to serialize playback status: %v", err)
			return
		}

		c.BroadcastTo("streamload", res)
	}
}

func (h *Handler) DeregisterClient(conn connection.Connection) error {
	err := h.clientHandler.DestroyClient(conn)
	if err != nil {
		return fmt.Errorf("error: unable to de-register client: %v", err)
	}
	return nil
}

func (h *Handler) getPlaybackFromClient(c *client.Client) (*playback.Playback, error) {
	ns, exists := c.Namespace()
	if !exists {
		return nil, fmt.Errorf("client with id (%q) has no room association. Ignoring streamsync request.", c.UUID())
	}

	sPlayback, exists := h.PlaybackHandler.PlaybackByNamespace(ns)
	if !exists {
		return nil, fmt.Errorf("warning: could not update stream playback. No room could be detected")
	}

	return sPlayback, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.server.ServeHTTP(w, r)
}

func NewHandler(nsHandler connection.NamespaceHandler, connHandler connection.ConnectionHandler, commandHandler cmd.SocketCommandHandler, clientHandler client.SocketClientHandler, playbackHandler playback.PlaybackHandler, streamHandler stream.StreamHandler) *Handler {
	handler := &Handler{
		clientHandler:   clientHandler,
		CommandHandler:  commandHandler,
		PlaybackHandler: playbackHandler,
		StreamHandler:   streamHandler,

		server: socketserver.NewServer(connHandler, nsHandler),
	}

	handler.addRequestHandlers()
	return handler
}

func (h *Handler) addRequestHandlers() {
	h.server.On("connection", func(conn connection.Connection) {
		h.HandleClientConnection(conn)
	})
}
