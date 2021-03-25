package gortsplib

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aler9/gortsplib/pkg/base"
	"github.com/aler9/gortsplib/pkg/headers"
	"github.com/aler9/gortsplib/pkg/multibuffer"
	"github.com/aler9/gortsplib/pkg/ringbuffer"
	"github.com/aler9/gortsplib/pkg/rtcpreceiver"
)

const (
	serverConnReadBufferSize         = 4096
	serverConnWriteBufferSize        = 4096
	serverConnCheckStreamInterval    = 5 * time.Second
	serverConnReceiverReportInterval = 10 * time.Second
)

// server errors.
var (
	ErrServerTeardown    = errors.New("teardown")
	errServerCSeqMissing = errors.New("CSeq is missing")
)

// ServerConnState is the state of the connection.
type ServerConnState int

// standard states.
const (
	ServerConnStateInitial ServerConnState = iota
	ServerConnStatePrePlay
	ServerConnStatePlay
	ServerConnStatePreRecord
	ServerConnStateRecord
)

// String implements fmt.Stringer.
func (s ServerConnState) String() string {
	switch s {
	case ServerConnStateInitial:
		return "initial"
	case ServerConnStatePrePlay:
		return "prePlay"
	case ServerConnStatePlay:
		return "play"
	case ServerConnStatePreRecord:
		return "preRecord"
	case ServerConnStateRecord:
		return "record"
	}
	return "unknown"
}

// ServerConnTrack is a setupped track of a ServerConn.
type ServerConnTrack struct {
	rtpPort  int
	rtcpPort int
}

// ServerConnAnnouncedTrack is an announced track of a ServerConn.
type ServerConnAnnouncedTrack struct {
	track            *Track
	rtcpReceiver     *rtcpreceiver.RTCPReceiver
	udpLastFrameTime *int64
}

// ServerConnReadHandlers allows to set the handlers required by ServerConn.Read.
// all fields are optional.
type ServerConnReadHandlers struct {
	// called after receiving any request.
	OnRequest func(req *base.Request)

	// called before sending any response.
	OnResponse func(res *base.Response)

	// called after receiving a OPTIONS request.
	// if nil, it is generated automatically.
	OnOptions func(req *base.Request) (*base.Response, error)

	// called after receiving a DESCRIBE request.
	OnDescribe func(req *base.Request) (*base.Response, error)

	// called after receiving an ANNOUNCE request.
	OnAnnounce func(req *base.Request, tracks Tracks) (*base.Response, error)

	// called after receiving a SETUP request.
	OnSetup func(req *base.Request, th *headers.Transport, trackID int) (*base.Response, error)

	// called after receiving a PLAY request.
	OnPlay func(req *base.Request) (*base.Response, error)

	// called after receiving a RECORD request.
	OnRecord func(req *base.Request) (*base.Response, error)

	// called after receiving a PAUSE request.
	OnPause func(req *base.Request) (*base.Response, error)

	// called after receiving a GET_PARAMETER request.
	// if nil, it is generated automatically.
	OnGetParameter func(req *base.Request) (*base.Response, error)

	// called after receiving a SET_PARAMETER request.
	OnSetParameter func(req *base.Request) (*base.Response, error)

	// called after receiving a TEARDOWN request.
	// if nil, it is generated automatically.
	OnTeardown func(req *base.Request) (*base.Response, error)

	// called after receiving a frame.
	OnFrame func(trackID int, streamType StreamType, payload []byte, stamp time.Time)
}

// ServerConn is a server-side RTSP connection.
type ServerConn struct {
	conf            ServerConf
	nconn           net.Conn
	udpRTPListener  *serverUDPListener
	udpRTCPListener *serverUDPListener
	br              *bufio.Reader
	bw              *bufio.Writer
	state           ServerConnState
	readHandlers    ServerConnReadHandlers
	tracks          map[int]ServerConnTrack
	streamProtocol  *StreamProtocol
	announcedTracks []ServerConnAnnouncedTrack

	doEnableFrames     bool
	framesEnabled      bool
	readTimeoutEnabled bool

	// writer
	frameRingBuffer     *ringbuffer.RingBuffer
	backgroundWriteDone chan struct{}

	// background record
	backgroundRecordTerminate chan struct{}
	backgroundRecordDone      chan struct{}
	udpTimeout                int32

	// in
	terminate chan struct{}
}

func newServerConn(conf ServerConf,
	udpRTPListener *serverUDPListener,
	udpRTCPListener *serverUDPListener,
	nconn net.Conn) *ServerConn {
	conn := func() net.Conn {
		if conf.TLSConfig != nil {
			return tls.Server(nconn, conf.TLSConfig)
		}
		return nconn
	}()

	return &ServerConn{
		conf:                conf,
		udpRTPListener:      udpRTPListener,
		udpRTCPListener:     udpRTCPListener,
		nconn:               nconn,
		br:                  bufio.NewReaderSize(conn, serverConnReadBufferSize),
		bw:                  bufio.NewWriterSize(conn, serverConnWriteBufferSize),
		frameRingBuffer:     ringbuffer.New(uint64(conf.ReadBufferCount)),
		backgroundWriteDone: make(chan struct{}),
		terminate:           make(chan struct{}),
	}
}

// Close closes all the connection resources.
func (sc *ServerConn) Close() error {
	err := sc.nconn.Close()
	close(sc.terminate)
	return err
}

// State returns the state.
func (sc *ServerConn) State() ServerConnState {
	return sc.state
}

// StreamProtocol returns the setupped tracks protocol.
func (sc *ServerConn) StreamProtocol() *StreamProtocol {
	return sc.streamProtocol
}

// HasTrack checks whether a track has been setup.
func (sc *ServerConn) HasTrack(trackID int) bool {
	_, ok := sc.tracks[trackID]
	return ok
}

// Tracks returns the setupped tracks.
func (sc *ServerConn) Tracks() map[int]ServerConnTrack {
	return sc.tracks
}

func (sc *ServerConn) backgroundWrite() {
	defer close(sc.backgroundWriteDone)

	for {
		what, ok := sc.frameRingBuffer.Pull()
		if !ok {
			return
		}

		switch w := what.(type) {
		case *base.InterleavedFrame:
			sc.nconn.SetWriteDeadline(time.Now().Add(sc.conf.WriteTimeout))
			w.Write(sc.bw)

		case *base.Response:
			sc.nconn.SetWriteDeadline(time.Now().Add(sc.conf.WriteTimeout))
			w.Write(sc.bw)

		default:
			panic(fmt.Errorf("unsupported type: %T", what))
		}
	}
}

func (sc *ServerConn) checkState(allowed map[ServerConnState]struct{}) error {
	if _, ok := allowed[sc.state]; ok {
		return nil
	}

	var allowedList []ServerConnState
	for a := range allowed {
		allowedList = append(allowedList, a)
	}
	return fmt.Errorf("must be in state %v, while is in state %v",
		allowedList, sc.state)
}

// NetConn returns the underlying net.Conn.
func (sc *ServerConn) NetConn() net.Conn {
	return sc.nconn
}

func (sc *ServerConn) ip() net.IP {
	return sc.nconn.RemoteAddr().(*net.TCPAddr).IP
}

func (sc *ServerConn) zone() string {
	return sc.nconn.RemoteAddr().(*net.TCPAddr).Zone
}

func (sc *ServerConn) frameModeEnable() {
	switch sc.state {
	case ServerConnStatePrePlay:
		if *sc.streamProtocol == StreamProtocolTCP {
			sc.doEnableFrames = true
		} else {
			// readers can send RTCP frames, they cannot sent RTP frames
			for trackID, track := range sc.tracks {
				sc.udpRTCPListener.addClient(sc.ip(), track.rtcpPort, sc, trackID, false)
				sc.udpRTPListener.addClient(sc.ip(), track.rtpPort, sc, trackID, false)
			}
		}

	case ServerConnStateRecord:
		if *sc.streamProtocol == StreamProtocolTCP {
			sc.doEnableFrames = true
			sc.readTimeoutEnabled = true

		} else {
			for trackID, track := range sc.tracks {
				sc.udpRTPListener.addClient(sc.ip(), track.rtpPort, sc, trackID, true)
				sc.udpRTCPListener.addClient(sc.ip(), track.rtcpPort, sc, trackID, true)

				// open the firewall by sending packets to the counterpart
				sc.WriteFrame(trackID, StreamTypeRTP,
					[]byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
				sc.WriteFrame(trackID, StreamTypeRTCP,
					[]byte{0x80, 0xc9, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00})
			}
		}

		sc.backgroundRecordTerminate = make(chan struct{})
		sc.backgroundRecordDone = make(chan struct{})
		go sc.backgroundRecord()
	}
}

func (sc *ServerConn) frameModeDisable() {
	switch sc.state {
	case ServerConnStatePlay:
		if *sc.streamProtocol == StreamProtocolTCP {
			sc.framesEnabled = false
			sc.frameRingBuffer.Close()
			<-sc.backgroundWriteDone

		} else {
			for _, track := range sc.tracks {
				sc.udpRTCPListener.removeClient(sc.ip(), track.rtcpPort)
				sc.udpRTPListener.removeClient(sc.ip(), track.rtpPort)
			}
		}

	case ServerConnStateRecord:
		close(sc.backgroundRecordTerminate)
		<-sc.backgroundRecordDone

		if *sc.streamProtocol == StreamProtocolTCP {
			sc.readTimeoutEnabled = false
			sc.nconn.SetReadDeadline(time.Time{})

			sc.framesEnabled = false
			sc.frameRingBuffer.Close()
			<-sc.backgroundWriteDone

		} else {
			for _, track := range sc.tracks {
				sc.udpRTPListener.removeClient(sc.ip(), track.rtpPort)
				sc.udpRTCPListener.removeClient(sc.ip(), track.rtcpPort)
			}
		}
	}
}

func (sc *ServerConn) handleRequest(req *base.Request) (*base.Response, error) {
	if cseq, ok := req.Header["CSeq"]; !ok || len(cseq) != 1 {
		return &base.Response{
			StatusCode: base.StatusBadRequest,
			Header:     base.Header{},
		}, errServerCSeqMissing
	}

	if sc.readHandlers.OnRequest != nil {
		sc.readHandlers.OnRequest(req)
	}

	switch req.Method {
	case base.Options:
		if sc.readHandlers.OnOptions != nil {
			return sc.readHandlers.OnOptions(req)
		}

		var methods []string
		if sc.readHandlers.OnDescribe != nil {
			methods = append(methods, string(base.Describe))
		}
		if sc.readHandlers.OnAnnounce != nil {
			methods = append(methods, string(base.Announce))
		}
		if sc.readHandlers.OnSetup != nil {
			methods = append(methods, string(base.Setup))
		}
		if sc.readHandlers.OnPlay != nil {
			methods = append(methods, string(base.Play))
		}
		if sc.readHandlers.OnRecord != nil {
			methods = append(methods, string(base.Record))
		}
		if sc.readHandlers.OnPause != nil {
			methods = append(methods, string(base.Pause))
		}
		methods = append(methods, string(base.GetParameter))
		if sc.readHandlers.OnSetParameter != nil {
			methods = append(methods, string(base.SetParameter))
		}
		methods = append(methods, string(base.Teardown))

		return &base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join(methods, ", ")},
			},
		}, nil

	case base.Describe:
		if sc.readHandlers.OnDescribe != nil {
			err := sc.checkState(map[ServerConnState]struct{}{
				ServerConnStateInitial: {},
			})
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			return sc.readHandlers.OnDescribe(req)
		}

	case base.Announce:
		if sc.readHandlers.OnAnnounce != nil {
			err := sc.checkState(map[ServerConnState]struct{}{
				ServerConnStateInitial: {},
			})
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			ct, ok := req.Header["Content-Type"]
			if !ok || len(ct) != 1 {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, errors.New("Content-Type header is missing")
			}

			if ct[0] != "application/sdp" {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("unsupported Content-Type '%s'", ct)
			}

			tracks, err := ReadTracks(req.Body, req.URL)
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("invalid SDP: %s", err)
			}

			if len(tracks) == 0 {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, errors.New("no tracks defined")
			}

			reqPath, ok := req.URL.RTSPPath()
			if !ok {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, errors.New("invalid path")
			}

			for _, track := range tracks {
				trackURL, err := track.URL()
				if err != nil {
					return &base.Response{
						StatusCode: base.StatusBadRequest,
					}, fmt.Errorf("invalid track URL")
				}

				trackPath, ok := trackURL.RTSPPath()
				if !ok {
					return &base.Response{
						StatusCode: base.StatusBadRequest,
					}, fmt.Errorf("invalid track URL")
				}

				if !strings.HasPrefix(trackPath, reqPath) {
					return &base.Response{
							StatusCode: base.StatusBadRequest,
						}, fmt.Errorf("invalid track URL: must begin with '%s', but is '%s'",
							reqPath, trackPath)
				}
			}

			res, err := sc.readHandlers.OnAnnounce(req, tracks)

			if res.StatusCode == 200 {
				sc.state = ServerConnStatePreRecord

				sc.announcedTracks = make([]ServerConnAnnouncedTrack, len(tracks))

				for trackID, track := range tracks {
					clockRate, _ := track.ClockRate()
					v := time.Now().Unix()

					sc.announcedTracks[trackID] = ServerConnAnnouncedTrack{
						track:            track,
						rtcpReceiver:     rtcpreceiver.New(nil, clockRate),
						udpLastFrameTime: &v,
					}
				}
			}

			return res, err
		}

	case base.Setup:
		if sc.readHandlers.OnSetup != nil {
			err := sc.checkState(map[ServerConnState]struct{}{
				ServerConnStateInitial:   {},
				ServerConnStatePrePlay:   {},
				ServerConnStatePreRecord: {},
			})
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			pathAndQuery, ok := req.URL.RTSPPathAndQuery()
			if !ok {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("invalid path (%s)", req.URL)
			}

			th, err := headers.ReadTransport(req.Header["Transport"])
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("transport header: %s", err)
			}

			switch sc.state {
			case ServerConnStateInitial, ServerConnStatePrePlay: // play
				if th.Mode != nil && *th.Mode != headers.TransportModePlay {
					return &base.Response{
						StatusCode: base.StatusBadRequest,
					}, fmt.Errorf("transport header must contain mode=play or not contain a mode")
				}

			default: // record
				if th.Mode == nil || *th.Mode != headers.TransportModeRecord {
					return &base.Response{
						StatusCode: base.StatusBadRequest,
					}, fmt.Errorf("transport header does not contain mode=record")
				}
			}

			if th.Delivery != nil && *th.Delivery == base.StreamDeliveryMulticast {
				return &base.Response{
					StatusCode: base.StatusUnsupportedTransport,
				}, nil
			}

			trackID, err := func() (int, error) {
				if th.Mode == nil || *th.Mode == headers.TransportModePlay {
					trackID, _, ok := base.PathSplitControlAttribute(pathAndQuery)
					if !ok {
						return 0, fmt.Errorf("invalid track (%s)", pathAndQuery)
					}

					return trackID, nil
				}

				for trackID, track := range sc.announcedTracks {
					u, _ := track.track.URL()
					if u.String() == req.URL.String() {
						return trackID, nil
					}
				}

				return 0, fmt.Errorf("invalid track (%s)", pathAndQuery)
			}()
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			if _, ok := sc.tracks[trackID]; ok {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("track %d has already been setup", trackID)
			}

			if sc.streamProtocol != nil && *sc.streamProtocol != th.Protocol {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("can't setup tracks with different protocols")
			}

			if th.Protocol == StreamProtocolUDP {
				if sc.udpRTPListener == nil {
					return &base.Response{
						StatusCode: base.StatusUnsupportedTransport,
					}, nil
				}

				if th.ClientPorts == nil {
					return &base.Response{
						StatusCode: base.StatusBadRequest,
					}, fmt.Errorf("transport header does not have valid client ports (%v)", req.Header["Transport"])
				}

			} else {
				if th.InterleavedIds == nil {
					return &base.Response{
						StatusCode: base.StatusBadRequest,
					}, fmt.Errorf("transport header does not contain the interleaved field")
				}

				if (*th.InterleavedIds)[0] != (trackID*2) ||
					(*th.InterleavedIds)[1] != (1+trackID*2) {
					return &base.Response{
							StatusCode: base.StatusBadRequest,
						}, fmt.Errorf("wrong interleaved ids, expected [%v %v], got %v",
							(trackID * 2), (1 + trackID*2), *th.InterleavedIds)
				}
			}

			res, err := sc.readHandlers.OnSetup(req, th, trackID)

			if res.StatusCode == 200 {
				sc.streamProtocol = &th.Protocol

				if sc.tracks == nil {
					sc.tracks = make(map[int]ServerConnTrack)
				}

				if th.Protocol == StreamProtocolUDP {
					sc.tracks[trackID] = ServerConnTrack{
						rtpPort:  th.ClientPorts[0],
						rtcpPort: th.ClientPorts[1],
					}

					if res.Header == nil {
						res.Header = make(base.Header)
					}
					res.Header["Transport"] = headers.Transport{
						Protocol: StreamProtocolUDP,
						Delivery: func() *base.StreamDelivery {
							v := base.StreamDeliveryUnicast
							return &v
						}(),
						ClientPorts: th.ClientPorts,
						ServerPorts: &[2]int{sc.udpRTPListener.port(), sc.udpRTCPListener.port()},
					}.Write()

				} else {
					sc.tracks[trackID] = ServerConnTrack{}

					if res.Header == nil {
						res.Header = make(base.Header)
					}
					res.Header["Transport"] = headers.Transport{
						Protocol:       StreamProtocolTCP,
						InterleavedIds: th.InterleavedIds,
					}.Write()
				}
			}

			switch sc.state {
			case ServerConnStateInitial:
				sc.state = ServerConnStatePrePlay
        sc.frameModeEnable()
			}

			// workaround to prevent a bug in rtspclientsink
			// that makes impossible for the client to receive the response
			// and send frames.
			// this was causing problems during unit tests.
			if ua, ok := req.Header["User-Agent"]; ok && len(ua) == 1 &&
				strings.HasPrefix(ua[0], "GStreamer") {
				t := time.NewTimer(1 * time.Second)
				defer t.Stop()
				select {
				case <-t.C:
				case <-sc.terminate:
				}
			}

			return res, err
		}

	case base.Play:
		if sc.readHandlers.OnPlay != nil {
			// play can be sent twice, allow calling it even if we're already playing
			err := sc.checkState(map[ServerConnState]struct{}{
				ServerConnStatePrePlay: {},
				ServerConnStatePlay:    {},
			})
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			if len(sc.tracks) == 0 {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("no tracks have been setup")
			}

			res, err := sc.readHandlers.OnPlay(req)

			if res.StatusCode == 200 && sc.state != ServerConnStatePlay {
				sc.state = ServerConnStatePlay
			}

			return res, err
		}

	case base.Record:
		if sc.readHandlers.OnRecord != nil {
			err := sc.checkState(map[ServerConnState]struct{}{
				ServerConnStatePreRecord: {},
			})
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			if len(sc.tracks) == 0 {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("no tracks have been setup")
			}

			if len(sc.tracks) != len(sc.announcedTracks) {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, fmt.Errorf("not all tracks have been setup")
			}

			res, err := sc.readHandlers.OnRecord(req)

			if res.StatusCode == 200 {
				sc.state = ServerConnStateRecord
				sc.frameModeEnable()
			}

			return res, err
		}

	case base.Pause:
		if sc.readHandlers.OnPause != nil {
			err := sc.checkState(map[ServerConnState]struct{}{
				ServerConnStatePrePlay:   {},
				ServerConnStatePlay:      {},
				ServerConnStatePreRecord: {},
				ServerConnStateRecord:    {},
			})
			if err != nil {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, err
			}

			res, err := sc.readHandlers.OnPause(req)

			if res.StatusCode == 200 {
				switch sc.state {
				case ServerConnStatePlay:
					sc.frameModeDisable()
					sc.state = ServerConnStatePrePlay

				case ServerConnStateRecord:
					sc.frameModeDisable()
					sc.state = ServerConnStatePreRecord
				}
			}

			return res, err
		}

	case base.GetParameter:
		if sc.readHandlers.OnGetParameter != nil {
			return sc.readHandlers.OnGetParameter(req)
		}

		// GET_PARAMETER is used like a ping
		return &base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"text/parameters"},
			},
			Body: []byte("\n"),
		}, nil

	case base.SetParameter:
		if sc.readHandlers.OnSetParameter != nil {
			return sc.readHandlers.OnSetParameter(req)
		}

	case base.Teardown:
		if sc.readHandlers.OnTeardown != nil {
			return sc.readHandlers.OnTeardown(req)
		}

		return &base.Response{
			StatusCode: base.StatusOK,
		}, ErrServerTeardown
	}

	return &base.Response{
		StatusCode: base.StatusBadRequest,
	}, fmt.Errorf("unhandled method: %v", req.Method)
}

func (sc *ServerConn) backgroundRead() error {
	var tcpFrameBuffer *multibuffer.MultiBuffer

	handleRequestOuter := func(req *base.Request) error {
		res, err := sc.handleRequest(req)

		if res.Header == nil {
			res.Header = base.Header{}
		}

		// add cseq
		if err != errServerCSeqMissing {
			res.Header["CSeq"] = req.Header["CSeq"]
		}

		// add server
		res.Header["Server"] = base.HeaderValue{"gortsplib"}

		if sc.readHandlers.OnResponse != nil {
			sc.readHandlers.OnResponse(res)
		}

		// start background write
		if sc.doEnableFrames {
			sc.doEnableFrames = false
			sc.framesEnabled = true

			if sc.state == ServerConnStateRecord {
				tcpFrameBuffer = multibuffer.New(uint64(sc.conf.ReadBufferCount), uint64(sc.conf.ReadBufferSize))
			} else {
				// when playing, tcpFrameBuffer is only used to receive RTCP receiver reports,
				// that are much smaller than RTP frames and are sent at a fixed interval
				// (about 2 frames every 10 secs).
				// decrease RAM consumption by allocating less buffers.
				tcpFrameBuffer = multibuffer.New(8, uint64(sc.conf.ReadBufferSize))
			}

			// write response before frames
			sc.nconn.SetWriteDeadline(time.Now().Add(sc.conf.WriteTimeout))
			res.Write(sc.bw)

			// start background write
			sc.frameRingBuffer.Reset()
			sc.backgroundWriteDone = make(chan struct{})
			go sc.backgroundWrite()

			// write to background write
		} else if sc.framesEnabled {
			sc.frameRingBuffer.Push(res)

			// write directly
		} else {
			sc.nconn.SetWriteDeadline(time.Now().Add(sc.conf.WriteTimeout))
			res.Write(sc.bw)
		}

		return err
	}

	var req base.Request
	var frame base.InterleavedFrame
	var errRet error

outer:
	for {
		if sc.readTimeoutEnabled {
			sc.nconn.SetReadDeadline(time.Now().Add(sc.conf.ReadTimeout))
		}

		if sc.framesEnabled {
			frame.Payload = tcpFrameBuffer.Next()
			what, err := base.ReadInterleavedFrameOrRequest(&frame, &req, sc.br)
			if err != nil {
				errRet = err
				break outer
			}

			switch what.(type) {
			case *base.InterleavedFrame:
				// forward frame only if it has been set up
				if _, ok := sc.tracks[frame.TrackID]; ok {
          stamp := time.Now()
					if sc.state == ServerConnStateRecord {
						sc.announcedTracks[frame.TrackID].rtcpReceiver.ProcessFrame(stamp,
							frame.StreamType, frame.Payload)
					}
					sc.readHandlers.OnFrame(frame.TrackID, frame.StreamType, frame.Payload, stamp)
				}

			case *base.Request:
				err := handleRequestOuter(&req)
				if err != nil {
					errRet = err
					break outer
				}
			}

		} else {
			err := req.Read(sc.br)
			if err != nil {
				if atomic.LoadInt32(&sc.udpTimeout) == 1 {
					errRet = fmt.Errorf("no UDP packets received recently (maybe there's a firewall/NAT in between)")
				} else {
					errRet = err
				}
				break outer
			}

			err = handleRequestOuter(&req)
			if err != nil {
				errRet = err
				break outer
			}
		}
	}

	sc.frameModeDisable()

	return errRet
}

// Read starts reading requests and frames.
// it returns a channel that is written when the reading stops.
func (sc *ServerConn) Read(readHandlers ServerConnReadHandlers) chan error {
	// channel is buffered, since listening to it is not mandatory
	done := make(chan error, 1)

	sc.readHandlers = readHandlers

	go func() {
		done <- sc.backgroundRead()
	}()

	return done
}

// WriteFrame writes a frame.
func (sc *ServerConn) WriteFrame(trackID int, streamType StreamType, payload []byte) {
	if *sc.streamProtocol == StreamProtocolUDP {
		track := sc.tracks[trackID]

		if streamType == StreamTypeRTP {
			sc.udpRTPListener.write(payload, &net.UDPAddr{
				IP:   sc.ip(),
				Zone: sc.zone(),
				Port: track.rtpPort,
			})
			return
		}

		sc.udpRTCPListener.write(payload, &net.UDPAddr{
			IP:   sc.ip(),
			Zone: sc.zone(),
			Port: track.rtcpPort,
		})
		return
	}

	// StreamProtocolTCP

	sc.frameRingBuffer.Push(&base.InterleavedFrame{
		TrackID:    trackID,
		StreamType: streamType,
		Payload:    payload,
	})
}

func (sc *ServerConn) backgroundRecord() {
	defer close(sc.backgroundRecordDone)

	checkStreamTicker := time.NewTicker(serverConnCheckStreamInterval)
	defer checkStreamTicker.Stop()

	receiverReportTicker := time.NewTicker(serverConnReceiverReportInterval)
	defer receiverReportTicker.Stop()

	for {
		select {
		case <-checkStreamTicker.C:
			if *sc.streamProtocol != StreamProtocolUDP {
				continue
			}

			now := time.Now()
			for _, track := range sc.announcedTracks {
				last := time.Unix(atomic.LoadInt64(track.udpLastFrameTime), 0)

				if now.Sub(last) >= sc.conf.ReadTimeout {
					atomic.StoreInt32(&sc.udpTimeout, 1)
					sc.nconn.Close()
					return
				}
			}

		case <-receiverReportTicker.C:
			now := time.Now()
			for trackID, track := range sc.announcedTracks {
				r := track.rtcpReceiver.Report(now)
				sc.WriteFrame(trackID, StreamTypeRTP, r)
			}

		case <-sc.backgroundRecordTerminate:
			return
		}
	}
}
