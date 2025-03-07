// Package base contains the base elements of the RTSP protocol.
package base

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
)

const (
	rtspProtocol10           = "RTSP/1.0"
	requestMaxMethodLength   = 64
	requestMaxURLLength      = 2048
	requestMaxProtocolLength = 64
)

// Method is the method of a RTSP request.
type Method string

// standard methods
const (
	Announce     Method = "ANNOUNCE"
	Describe     Method = "DESCRIBE"
	GetParameter Method = "GET_PARAMETER"
	Options      Method = "OPTIONS"
	Pause        Method = "PAUSE"
	Play         Method = "PLAY"
	Record       Method = "RECORD"
	Setup        Method = "SETUP"
	SetParameter Method = "SET_PARAMETER"
	Teardown     Method = "TEARDOWN"
)

// Request is a RTSP request.
type Request struct {
	// request method
	Method Method

	// request url
	URL *URL

	// map of header values
	Header Header

	// optional body
	Body []byte

	// whether to wait for a response or not
	// used only by ClientConn.Do()
	SkipResponse bool
}

// Read reads a request.
func (req *Request) Read(rb *bufio.Reader) error {
	byts, err := readBytesLimited(rb, ' ', requestMaxMethodLength)
	if err != nil {
		return err
	}
	req.Method = Method(byts[:len(byts)-1])

	if req.Method == "" {
		return fmt.Errorf("empty method")
	}

	byts, err = readBytesLimited(rb, ' ', requestMaxURLLength)
	if err != nil {
		return err
	}
	rawURL := string(byts[:len(byts)-1])

	ur, err := ParseURL(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL (%v)", rawURL)
	}
	req.URL = ur

	byts, err = readBytesLimited(rb, '\r', requestMaxProtocolLength)
	if err != nil {
		return err
	}
	proto := string(byts[:len(byts)-1])

	if proto != rtspProtocol10 {
		return fmt.Errorf("expected '%s', got '%s'", rtspProtocol10, proto)
	}

	err = readByteEqual(rb, '\n')
	if err != nil {
		return err
	}

	err = req.Header.read(rb)
	if err != nil {
		return err
	}

	err = (*payload)(&req.Body).read(rb, req.Header)
	if err != nil {
		return err
	}

	return nil
}

// ReadIgnoreFrames reads a request and ignores any interleaved frame sent
// before the request.
func (req *Request) ReadIgnoreFrames(rb *bufio.Reader, buf []byte) error {
	buflen := len(buf)
	f := InterleavedFrame{
		Payload: buf,
	}

	for {
		f.Payload = f.Payload[:buflen]
		recv, err := ReadInterleavedFrameOrRequest(&f, req, rb)
		if err != nil {
			return err
		}

		if _, ok := recv.(*Request); ok {
			return nil
		}
	}
}

// Write writes a request.
func (req Request) Write(bw *bufio.Writer) error {
	urStr := req.URL.CloneWithoutCredentials().String()
	_, err := bw.Write([]byte(string(req.Method) + " " + urStr + " " + rtspProtocol10 + "\r\n"))
	if err != nil {
		return err
	}

	if len(req.Body) != 0 {
		req.Header["Content-Length"] = HeaderValue{strconv.FormatInt(int64(len(req.Body)), 10)}
	}

	err = req.Header.write(bw)
	if err != nil {
		return err
	}

	err = payload(req.Body).write(bw)
	if err != nil {
		return err
	}

	return bw.Flush()
}

// String implements fmt.Stringer.
func (req Request) String() string {
	buf := bytes.NewBuffer(nil)
	req.Write(bufio.NewWriter(buf))
	return buf.String()
}
