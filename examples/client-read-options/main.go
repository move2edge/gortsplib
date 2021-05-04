package main

import (
	"fmt"
	"time"

	"github.com/aler9/gortsplib"
)

// This example shows how to
// 1. set additional client options
// 2. connect to a RTSP server and read all tracks on a path

func main() {
	// Client allows to set additional client options
	c := &gortsplib.Client{
		// the stream protocol (UDP or TCP). If nil, it is chosen automatically
		StreamProtocol: nil,
		// timeout of read operations
		ReadTimeout: 10 * time.Second,
		// timeout of write operations
		WriteTimeout: 10 * time.Second,
	}

	// connect to the server and start reading all tracks
	conn, err := c.DialRead("rtsp://localhost:8554/mystream")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	// read RTP frames
	err = <-conn.ReadFrames(func(trackID int, typ gortsplib.StreamType, buf []byte) {
		fmt.Printf("frame from track %d, type %v, size %d\n", trackID, typ, len(buf))
	})
	panic(err)
}
