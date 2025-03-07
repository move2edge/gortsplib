package gortsplib

import (
	"bufio"
	"crypto/tls"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aler9/gortsplib/pkg/base"
	"github.com/aler9/gortsplib/pkg/headers"
	"github.com/aler9/gortsplib/pkg/liberrors"
	"github.com/aler9/gortsplib/pkg/multibuffer"
	"github.com/aler9/gortsplib/pkg/ringbuffer"
	"github.com/aler9/gortsplib/pkg/rtcpreceiver"
)

const (
	serverConnReadBufferSize    = 4096
	serverConnWriteBufferSize   = 4096
	serverConnCheckStreamPeriod = 5 * time.Second
)

func stringsReverseIndex(s, substr string) int {
	for i := len(s) - 1 - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func setupGetTrackIDPathQuery(url *base.URL,
	thMode *headers.TransportMode,
	announcedTracks []ServerConnAnnouncedTrack,
	setupPath *string, setupQuery *string) (int, string, string, error) {

	pathAndQuery, ok := url.RTSPPathAndQuery()
	if !ok {
		return 0, "", "", liberrors.ErrServerNoPath{}
	}

	if thMode == nil || *thMode == headers.TransportModePlay {
		i := stringsReverseIndex(pathAndQuery, "/trackID=")

		// URL doesn't contain trackID - it's track zero
		if i < 0 {
			if !strings.HasSuffix(pathAndQuery, "/") {
				return 0, "", "", fmt.Errorf("path must end with a slash (%v)", pathAndQuery)
			}
			pathAndQuery = pathAndQuery[:len(pathAndQuery)-1]

			path, query := base.PathSplitQuery(pathAndQuery)

			// we assume it's track 0
			return 0, path, query, nil
		}

		tmp, err := strconv.ParseInt(pathAndQuery[i+len("/trackID="):], 10, 64)
		if err != nil || tmp < 0 {
			return 0, "", "", fmt.Errorf("unable to parse track ID (%v)", pathAndQuery)
		}
		trackID := int(tmp)
		pathAndQuery = pathAndQuery[:i]

		path, query := base.PathSplitQuery(pathAndQuery)

		if setupPath != nil && (path != *setupPath || query != *setupQuery) {
			return 0, "", "", fmt.Errorf("can't setup tracks with different paths")
		}

		return trackID, path, query, nil
	}

	for trackID, track := range announcedTracks {
		u, _ := track.track.URL()
		if u.String() == url.String() {
			return trackID, *setupPath, *setupQuery, nil
		}
	}

	return 0, "", "", fmt.Errorf("invalid track path (%s)", pathAndQuery)
}

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

// ServerConnSetuppedTrack is a setupped track of a ServerConn.
type ServerConnSetuppedTrack struct {
	udpRTPPort  int
	udpRTCPPort int
}

// ServerConnAnnouncedTrack is an announced track of a ServerConn.
type ServerConnAnnouncedTrack struct {
	track            *Track
	rtcpReceiver     *rtcpreceiver.RTCPReceiver
	udpLastFrameTime *int64
}

// ServerConnOptionsCtx is the context of a OPTIONS request.
type ServerConnOptionsCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnDescribeCtx is the context of a DESCRIBE request.
type ServerConnDescribeCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnAnnounceCtx is the context of a ANNOUNCE request.
type ServerConnAnnounceCtx struct {
	Req    *base.Request
	Path   string
	Query  string
	Tracks Tracks
}

// ServerConnSetupCtx is the context of a OPTIONS request.
type ServerConnSetupCtx struct {
	Req       *base.Request
	Path      string
	Query     string
	TrackID   int
	Transport *headers.Transport
}

// ServerConnPlayCtx is the context of a PLAY request.
type ServerConnPlayCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnRecordCtx is the context of a RECORD request.
type ServerConnRecordCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnPauseCtx is the context of a PAUSE request.
type ServerConnPauseCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnGetParameterCtx is the context of a GET_PARAMETER request.
type ServerConnGetParameterCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnSetParameterCtx is the context of a SET_PARAMETER request.
type ServerConnSetParameterCtx struct {
	Req   *base.Request
	Path  string
	Query string
}

// ServerConnTeardownCtx is the context of a TEARDOWN request.
type ServerConnTeardownCtx struct {
	Req   *base.Request
	Path  string
	Query string
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
	OnOptions func(ctx *ServerConnOptionsCtx) (*base.Response, error)

	// called after receiving a DESCRIBE request.
	// the 2nd return value is a SDP, that is inserted into the response.
	OnDescribe func(ctx *ServerConnDescribeCtx) (*base.Response, []byte, error)

	// called after receiving an ANNOUNCE request.
	OnAnnounce func(ctx *ServerConnAnnounceCtx) (*base.Response, error)

	// called after receiving a SETUP request.
	OnSetup func(ctx *ServerConnSetupCtx) (*base.Response, error)

	// called after receiving a PLAY request.
	OnPlay func(ctx *ServerConnPlayCtx) (*base.Response, error)

	// called after receiving a RECORD request.
	OnRecord func(ctx *ServerConnRecordCtx) (*base.Response, error)

	// called after receiving a PAUSE request.
	OnPause func(ctx *ServerConnPauseCtx) (*base.Response, error)

	// called after receiving a GET_PARAMETER request.
	// if nil, it is generated automatically.
	OnGetParameter func(ctx *ServerConnGetParameterCtx) (*base.Response, error)

	// called after receiving a SET_PARAMETER request.
	OnSetParameter func(ctx *ServerConnSetParameterCtx) (*base.Response, error)

	// called after receiving a TEARDOWN request.
	// if nil, it is generated automatically.
	OnTeardown func(ctx *ServerConnTeardownCtx) (*base.Response, error)

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
	setuppedTracks  map[int]ServerConnSetuppedTrack
	setupProtocol   *StreamProtocol
	setupPath       *string
	setupQuery      *string

	// TCP stream protocol
	doEnableTCPFrame       bool
	tcpFrameEnabled        bool
	tcpFrameTimeout        bool
	tcpFrameBuffer         *multibuffer.MultiBuffer
	tcpFrameWriteBuffer    *ringbuffer.RingBuffer
	tcpBackgroundWriteDone chan struct{}

	// read
	readHandlers ServerConnReadHandlers

	// publish
	announcedTracks           []ServerConnAnnouncedTrack
	backgroundRecordTerminate chan struct{}
	backgroundRecordDone      chan struct{}
	udpTimeout                int32

	// in
	innerTerminate chan struct{}
	terminate      chan struct{}
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

	sc := &ServerConn{
		s:              s,
		wg:             wg,
		nconn:          nconn,
		innerTerminate: make(chan struct{}, 1),
		terminate:      make(chan struct{}),
	}
}

// Close closes all the connection resources.
func (sc *ServerConn) Close() error {
	err := sc.nconn.Close()
	close(sc.terminate)
	return err
}

// Close closes the ServerConn.
func (sc *ServerConn) Close() error {
	select {
	case sc.innerTerminate <- struct{}{}:
	default:
	}
	return nil
}

// NetConn returns the underlying net.Conn.
func (sc *ServerConn) NetConn() net.Conn {
	return sc.nconn
}

// StreamProtocol returns the stream protocol of the setupped tracks.
func (sc *ServerConn) StreamProtocol() *StreamProtocol {
	return sc.setupProtocol
}

// SetuppedTracks returns the setupped tracks.
func (sc *ServerConn) SetuppedTracks() map[int]ServerConnSetuppedTrack {
	return sc.setuppedTracks
}

// AnnouncedTracks returns the announced tracks.
func (sc *ServerConn) AnnouncedTracks() []ServerConnAnnouncedTrack {
	return sc.announcedTracks
}

func (sc *ServerConn) tcpBackgroundWrite() {
	defer close(sc.tcpBackgroundWriteDone)

	for {
		what, ok := sc.tcpFrameWriteBuffer.Pull()
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
		}
	}
}

func (sc *ServerConn) checkState(allowed map[ServerConnState]struct{}) error {
	if _, ok := allowed[sc.state]; ok {
		return nil
	}

	allowedList := make([]fmt.Stringer, len(allowed))
	i := 0
	for a := range allowed {
		allowedList[i] = a
		i++
	}
	return liberrors.ErrServerWrongState{AllowedList: allowedList, State: sc.state}
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
	case ServerConnStatePlay:
		if *sc.setupProtocol == StreamProtocolTCP {
			sc.doEnableTCPFrame = true
		} else {
			// readers can send RTCP frames, they cannot sent RTP frames
			for trackID, track := range sc.setuppedTracks {
				sc.udpRTCPListener.addClient(sc.ip(), track.udpRTCPPort, sc, trackID, false)
			}
		}

			sc.nconn.Close()

			sc.s.connClose <- sc
			<-sc.terminate

		sc.backgroundRecordTerminate = make(chan struct{})
		sc.backgroundRecordDone = make(chan struct{})
		go sc.backgroundRecord()
	}
}

		case <-sc.innerTerminate:
			sc.nconn.Close()
			<-readDone

			if sc.tcpFrameEnabled {
				sc.tcpFrameWriteBuffer.Close()
				<-sc.tcpFrameBackgroundWriteDone
			}

			sc.s.connClose <- sc
			<-sc.terminate

			return liberrors.ErrServerTerminated{}

		case <-sc.terminate:
			sc.nconn.Close()
			<-readDone

		} else {
			for _, track := range sc.setuppedTracks {
				sc.udpRTCPListener.removeClient(sc.ip(), track.udpRTCPPort)
			}
		}

	case ServerConnStateRecord:
		close(sc.backgroundRecordTerminate)
		<-sc.backgroundRecordDone

		if *sc.setupProtocol == StreamProtocolTCP {
			sc.tcpFrameTimeout = false
			sc.nconn.SetReadDeadline(time.Time{})

			sc.tcpFrameEnabled = false
			sc.tcpFrameWriteBuffer.Close()
			<-sc.tcpBackgroundWriteDone
			sc.tcpFrameWriteBuffer.Reset()

		} else {
			for _, track := range sc.setuppedTracks {
				sc.udpRTPListener.removeClient(sc.ip(), track.udpRTPPort)
				sc.udpRTCPListener.removeClient(sc.ip(), track.udpRTCPPort)
			}
		}
	}
}

func (sc *ServerConn) handleRequest(req *base.Request) (*base.Response, error) {
	if cseq, ok := req.Header["CSeq"]; !ok || len(cseq) != 1 {
		return &base.Response{
			StatusCode: base.StatusBadRequest,
			Header:     base.Header{},
		}, liberrors.ErrServerCSeqMissing{}
	}

	if sc.readHandlers.OnRequest != nil {
		sc.readHandlers.OnRequest(req)
	}

	switch req.Method {
	case base.Options:
		// handle request in session
		if sxID != "" {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: false,
				res:    cres,
			}
			res := <-cres
			return res.res, res.err
		}

		// handle request here
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

			pathAndQuery, ok := req.URL.RTSPPath()
			if !ok {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, liberrors.ErrServerNoPath{}
			}

			path, query := base.PathSplitQuery(pathAndQuery)

			res, sdp, err := sc.readHandlers.OnDescribe(&ServerConnDescribeCtx{
				Req:   req,
				Path:  path,
				Query: query,
			})

			if res.StatusCode == base.StatusOK && sdp != nil {
				if res.Header == nil {
					res.Header = make(base.Header)
				}

				res.Header["Content-Base"] = base.HeaderValue{req.URL.String() + "/"}
				res.Header["Content-Type"] = base.HeaderValue{"application/sdp"}
				res.Body = sdp
			}

			return res, err
		}

	case base.Announce:
		if _, ok := sc.s.Handler.(ServerHandlerOnAnnounce); ok {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: true,
				res:    cres,
			}
			res := <-cres
			return res.res, res.err
		}

	case base.Setup:
		if _, ok := sc.s.Handler.(ServerHandlerOnSetup); ok {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: true,
				res:    cres,
			}
			res := <-cres
			return res.res, res.err
		}

	case base.Play:
		if _, ok := sc.s.Handler.(ServerHandlerOnPlay); ok {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: false,
				res:    cres,
			}
			res := <-cres

			if _, ok := res.err.(liberrors.ErrServerTCPFramesEnable); ok {
				sc.tcpFrameLinkedSession = res.ss
				sc.tcpFrameIsRecording = false
				sc.tcpFrameSetEnabled = true
				return res.res, nil
			}

			return res, err
		}

	case base.Record:
		if _, ok := sc.s.Handler.(ServerHandlerOnRecord); ok {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: false,
				res:    cres,
			}
			res := <-cres

			if _, ok := res.err.(liberrors.ErrServerTCPFramesEnable); ok {
				sc.tcpFrameLinkedSession = res.ss
				sc.tcpFrameIsRecording = true
				sc.tcpFrameSetEnabled = true
				return res.res, nil
			}

			return res, err
		}

	case base.Pause:
		if _, ok := sc.s.Handler.(ServerHandlerOnPause); ok {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: false,
				res:    cres,
			}
			res := <-cres

			path, query := base.PathSplitQuery(pathAndQuery)

			res, err := sc.readHandlers.OnPause(&ServerConnPauseCtx{
				Req:   req,
				Path:  path,
				Query: query,
			})

	case base.Teardown:
		cres := make(chan sessionReqRes)
		sc.s.sessionReq <- sessionReq{
			sc:     sc,
			req:    req,
			id:     sxID,
			create: false,
			res:    cres,
		}
		res := <-cres
		return res.res, res.err

	case base.GetParameter:
		// handle request in session
		if sxID != "" {
			cres := make(chan sessionReqRes)
			sc.s.sessionReq <- sessionReq{
				sc:     sc,
				req:    req,
				id:     sxID,
				create: false,
				res:    cres,
			}
			res := <-cres
			return res.res, res.err
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
			pathAndQuery, ok := req.URL.RTSPPath()
			if !ok {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, liberrors.ErrServerNoPath{}
			}

			path, query := base.PathSplitQuery(pathAndQuery)

			return sc.readHandlers.OnSetParameter(&ServerConnSetParameterCtx{
				Req:   req,
				Path:  path,
				Query: query,
			})
		}

	case base.Teardown:
		if sc.readHandlers.OnTeardown != nil {
			pathAndQuery, ok := req.URL.RTSPPath()
			if !ok {
				return &base.Response{
					StatusCode: base.StatusBadRequest,
				}, liberrors.ErrServerNoPath{}
			}

			path, query := base.PathSplitQuery(pathAndQuery)

			return sc.readHandlers.OnTeardown(&ServerConnTeardownCtx{
				Req:   req,
				Path:  path,
				Query: query,
			})
		}

		return &base.Response{
			StatusCode: base.StatusOK,
		}, liberrors.ErrServerTeardown{}
	}

	return &base.Response{
		StatusCode: base.StatusBadRequest,
	}, liberrors.ErrServerUnhandledRequest{Req: req}
}

func (sc *ServerConn) handleRequestOuter(req *base.Request) error {
	if h, ok := sc.s.Handler.(ServerHandlerOnRequest); ok {
		h.OnRequest(sc, req)
	}

	res, err := sc.handleRequest(req)

	if res.Header == nil {
		res.Header = base.Header{}
	}

	// add cseq
	if _, ok := err.(liberrors.ErrServerCSeqMissing); !ok {
		res.Header["CSeq"] = req.Header["CSeq"]
	}

	// add server
	res.Header["Server"] = base.HeaderValue{"gortsplib"}

	if h, ok := sc.s.Handler.(ServerHandlerOnResponse); ok {
		h.OnResponse(sc, res)
	}

	switch {
	case sc.doEnableTCPFrame: // start background write
		sc.doEnableTCPFrame = false
		sc.tcpFrameEnabled = true

		if sc.state == ServerConnStateRecord {
			sc.tcpFrameBuffer = multibuffer.New(uint64(sc.conf.ReadBufferCount), uint64(sc.conf.ReadBufferSize))
		} else {
			// when playing, tcpFrameBuffer is only used to receive RTCP receiver reports,
			// that are much smaller than RTP frames and are sent at a fixed interval
			// (about 2 frames every 10 secs).
			// decrease RAM consumption by allocating less buffers.
			sc.tcpFrameBuffer = multibuffer.New(8, uint64(sc.conf.ReadBufferSize))
		}

		// write response before frames
		sc.nconn.SetWriteDeadline(time.Now().Add(sc.conf.WriteTimeout))
		res.Write(sc.bw)

		// start background write
		sc.tcpBackgroundWriteDone = make(chan struct{})
		go sc.tcpBackgroundWrite()

	case sc.tcpFrameEnabled: // write to background write
		sc.tcpFrameWriteBuffer.Push(res)

	default: // write directly
		sc.nconn.SetWriteDeadline(time.Now().Add(sc.conf.WriteTimeout))
		res.Write(sc.bw)
	}

	return err
}

func (sc *ServerConn) backgroundRead() error {
	defer sc.frameModeDisable()

	var req base.Request
	var frame base.InterleavedFrame

	for {
		if sc.tcpFrameEnabled {
			if sc.tcpFrameTimeout {
				sc.nconn.SetReadDeadline(time.Now().Add(sc.conf.ReadTimeout))
			}

			frame.Payload = sc.tcpFrameBuffer.Next()
			what, err := base.ReadInterleavedFrameOrRequest(&frame, &req, sc.br)
			if err != nil {
				return err
			}

			switch what.(type) {
			case *base.InterleavedFrame:
				// forward frame only if it has been set up
				if _, ok := sc.setuppedTracks[frame.TrackID]; ok {
					if sc.state == ServerConnStateRecord {
						sc.announcedTracks[frame.TrackID].rtcpReceiver.ProcessFrame(time.Now(),
							frame.StreamType, frame.Payload)
					}
					sc.readHandlers.OnFrame(frame.TrackID, frame.StreamType, frame.Payload)
				}

			case *base.Request:
				err := sc.handleRequestOuter(&req)
				if err != nil {
					return err
				}
			}

		} else {
			err := req.Read(sc.br)
			if err != nil {
				if atomic.LoadInt32(&sc.udpTimeout) == 1 {
					return liberrors.ErrServerNoUDPPacketsRecently{}
				}
				return err
			}

			err = sc.handleRequestOuter(&req)
			if err != nil {
				return err
			}
		}
	}
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
	if *sc.setupProtocol == StreamProtocolUDP {
		track := sc.setuppedTracks[trackID]

		if streamType == StreamTypeRTP {
			sc.udpRTPListener.write(payload, &net.UDPAddr{
				IP:   sc.ip(),
				Zone: sc.zone(),
				Port: track.udpRTPPort,
			})
			return
		}

		sc.udpRTCPListener.write(payload, &net.UDPAddr{
			IP:   sc.ip(),
			Zone: sc.zone(),
			Port: track.udpRTCPPort,
		})
		return
	}

	sc.tcpFrameWriteBuffer.Push(&base.InterleavedFrame{
		TrackID:    trackID,
		StreamType: streamType,
		Payload:    payload,
	})
}

func (sc *ServerConn) backgroundRecord() {
	defer close(sc.backgroundRecordDone)

	checkStreamTicker := time.NewTicker(serverConnCheckStreamPeriod)
	defer checkStreamTicker.Stop()

	receiverReportTicker := time.NewTicker(sc.conf.receiverReportPeriod)
	defer receiverReportTicker.Stop()

	for {
		select {
		case <-checkStreamTicker.C:
			if *sc.setupProtocol != StreamProtocolUDP {
				continue
			}

			inTimeout := func() bool {
				now := time.Now()
				for _, track := range sc.announcedTracks {
					lft := atomic.LoadInt64(track.udpLastFrameTime)
					if now.Sub(time.Unix(lft, 0)) < sc.conf.ReadTimeout {
						return false
					}
				}
				return true
			}()
			if inTimeout {
				atomic.StoreInt32(&sc.udpTimeout, 1)
				sc.nconn.Close()
				return
			}

		case <-receiverReportTicker.C:
			now := time.Now()
			for trackID, track := range sc.announcedTracks {
				r := track.rtcpReceiver.Report(now)
				sc.WriteFrame(trackID, StreamTypeRTCP, r)
			}

		case <-sc.backgroundRecordTerminate:
			return
		}
	}
}
