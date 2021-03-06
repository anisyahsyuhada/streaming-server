Streaming Server
================

![Working demo at FlickTrack.me](https://i.imgur.com/YhJ1sUP.jpg)

## What?

Multi-client media streaming server. Watch videos with friends, keeping everyone in sync.

Please [see the Wiki for a detailed guide](https://github.com/juanvallejo/streaming-server/wiki) on using this project.

## Why?

Thought it'd be fun.

## Downloading

There are currently no downloads available, links will appear here in the future for binary downloads of this project. Please see the Compiling section to learn how to compile this project for now.

## Building

Alternatively, you may choose to compile this project yourself rather than downloading a pre-compiled binary. See the sub-sections below for detailed steps and requirements.

### Compiling

Requires [Go](https://golang.org/) and [ffmpeg-devel](https://www.ffmpeg.org/download.html) to compile.

1. Either `git clone` or `go get` this repo.
  - `cd $GOPATH && go get github.com/juanvallejo/streaming-server`
  - **or** (using `git clone`):
    - `mkdir -p $GOPATH/src/github.com/juanvallejo`
    - `cd $GOPATH/src/github.com/juanvallejo`
	- `git clone https://github.com/juanvallejo/streaming-server`
    - `git submodule update --init` (init the webclient submodule)
2. `make`

### Running

Once you've followed these steps, you should see a newly created `bin` directory containing a `streaming` binary.
 1. `./bin/streaming`
   - You can optionally specify the port to bind to with `./bin/streaming --port <PORT>`
 
The server will bind to port `8080` by default. Once it is running, you can access the web client at `http://localhost:8080`.
To access a stream room, create a room by going to `http://localhost:8080/v/roomname`.

## Further reading

### Wiki

For up-to-date and comprehensive information about using this project, please read [the wiki](https://github.com/juanvallejo/streaming-server/wiki).

### Simple Overview

#### Rooms

`Rooms` represent individual playback objects that allow one or more clients to play media independent from each other.

The default room url is `/v/<ROOM>`.

A room is created the first time it is accessed. Any subsequent access, retrieves previously created playback information.

#### Chat

Each room contains a chat, which acts as a make-shift command prompt.  
Any chat messages entered in the chat beginning with `/` are interpreted as commands by the server.

A list of available commands is available by typing `/help` in the chat.

#### Streams

Playback requires a `stream` to be set, before operations such as `play`, `pause`, `stop` can be performed.  
To set a `stream`, enter the command `/stream set <URL>` in the chat.

Playback, right now, supports streaming `youtube` and `local` videos.

##### Streaming local videos & "data" directory

`Local` videos are streamed from local files in the server. These files should be placed in the `data` directory, in the root of this project.

Example of setting a local video:
```
/stream set mylocalvideo.mp4
```

Note that local video files must be of a browser-friendly format in order for them to actually be displayed.

##### Streaming youtube videos

`youtube` videos are set using their full YouTube url.

Example of setting a `youtube` video:
```
/stream set https://www.youtube.com/watch?v=...
```

#### Controlling a stream

Once you `set` a stream, it will be loaded by each client, but will not begin playing.  
To begin playback, use the command:
```
/stream play
```

Performing this command before setting a stream will result in a playback error.

#### The queue

*Please refer to [the wiki](https://github.com/juanvallejo/streaming-server/wiki) for a comprehensive guide and explanation of the queue.*

It is also possible to queue media. Queuing differs from the `/stream set ...` command in that `set` immediately loads a video, bypassing the queue entirely, 
while `queueing` simply pushes a stream object into an internal list.

You can queue a video with:
```
/queue add <URL>
```

Note that `queuing` does not cause clients to also load the video. For the next item in the queue to be `loaded` by all clients,
you must `skip` the currently set video with:
```
/stream skip
```

Note that this command results in an error if there are no items in the queue.  
Additionally, by default, clients will automatically load the next item in the queue, if one exists, once the currently playing video ends.

### TODO

- ~~Add ui for commonly used commands that control stream playback and queue actions.~~
- Add a client `config` that allows certain features to be turned on or off (such as automatically playing the next item in the queue).
