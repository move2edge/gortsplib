package gortsplib

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aler9/gortsplib/pkg/multibuffer"
	"github.com/aler9/gortsplib/pkg/ringbuffer"
	. "github.com/logrusorgru/aurora"
	log "github.com/sirupsen/logrus"
)

const (
	serverConnUDPListenerKernelReadBufferSize = 0x80000 // same as gstreamer's rtspsrc
)

type bufAddrPair struct {
	buf  []byte
	addr *net.UDPAddr
}

type clientData struct {
	ss           *ServerSession
	trackID      int
	isPublishing bool
	streamType   StreamType
	isMapped     bool
}

type clientAddr struct {
	ip   [net.IPv6len]byte // use a fixed-size array to enable the equality operator
	port int
}

func (s *serverUDPListener) LogInfo(msg ...interface{}) {
	myAddr := s.pc.LocalAddr()
	log.Info(Magenta(myAddr), " ", fmt.Sprint(msg...))
}

func (p *clientAddr) fill(ip net.IP, port int) {
	p.port = port

	if len(ip) == net.IPv4len {
		copy(p.ip[0:], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff}) // v4InV6Prefix
		copy(p.ip[12:], ip)
	} else {
		copy(p.ip[:], ip)
	}
}

func (s *serverUDPListener) printClientList(title string, highlight clientAddr) {
	mylog := fmt.Sprintln(Cyan("LIST "), title, ":")
	var pubStr string
	var addrStr string
	for address, client := range s.clients {
		mapStr := " "
		if client.isMapped {
			mapStr = "M"
		}
		if client.isPublishing {
			pubStr = Red("P").String()
		} else {
			pubStr = Green("C").String()
		}
		if address == highlight {
			addrStr = Yellow(fmt.Sprint(address.ip[(len(address.ip)-4):], address.port)).String()
		} else {
			addrStr = fmt.Sprint(address.ip[(len(address.ip)-4):], address.port)
		}
		mylog = mylog + fmt.Sprintln("    ", pubStr, Blue(mapStr), addrStr)
	}
	s.LogInfo(mylog)
}

func (s *serverUDPListener) printMappingInfo(address clientAddr, client *clientData, incoming *net.UDPAddr) {
	var oldPort int
	var pubStr string
	if client.isPublishing {
		pubStr = "P"
	} else {
		pubStr = "C"
	}
	if client.streamType == StreamTypeRTP {
		oldPort = client.sc.setuppedTracks[client.trackID].udpRTPPort
	} else {
		oldPort = client.sc.setuppedTracks[client.trackID].udpRTCPPort
	}
	s.LogInfo(Yellow("MAP:"), pubStr, client.streamType,
		address.ip[(len(address.ip)-4):], oldPort, "->", incoming.IP, incoming.Port)
}

func (s *serverUDPListener) updatePorts(isPublisher bool, streamType StreamType, addr *net.UDPAddr) (*clientData, error) {
	var clientData *clientData
	for address, client := range s.clients {
		if client.isPublishing == isPublisher && client.streamType == streamType {
			if isPublisher == false && client.isMapped == true {
				continue
			}
			s.printMappingInfo(address, client, addr)
			var track ServerConnSetuppedTrack
			if streamType == StreamTypeRTP {
				track.udpRTPPort = addr.Port
				track.udpRTCPPort = client.sc.setuppedTracks[client.trackID].udpRTCPPort
			} else {
				track.udpRTPPort = client.sc.setuppedTracks[client.trackID].udpRTPPort
				track.udpRTCPPort = addr.Port
			}
			client.sc.setuppedTracks[client.trackID] = track
			clientData = client
			delete(s.clients, address)
			var clientAddr clientAddr
			clientAddr.fill(addr.IP, addr.Port)
			s.clients[clientAddr] = clientData
			client.isMapped = true
			s.printClientList(fmt.Sprintf("%s", Yellow("map")), clientAddr)
			return clientData, nil
		}
	}
	log.Errorln("Not found in address table", addr)
	return nil, errors.New("Not found in address table")
}

func (s *serverUDPListener) handleNat(n int, addr *net.UDPAddr, buf []byte) (*clientData, error) {
	var clientData *clientData
	var err error = nil
	log.Debugln("Unknown frame", addr.IP, addr.Port, n, buf[:8], s.streamType)
	if buf[1] == 200 {
		clientData, err = s.updatePorts(true, s.streamType, addr)
	} else if buf[1] == 201 {
		clientData, err = s.updatePorts(false, s.streamType, addr)
	} else if buf[1] == 0 {
		clientData, err = s.updatePorts(false, s.streamType, addr)
	} else if buf[1] == 224 {
		clientData, err = s.updatePorts(true, s.streamType, addr)
	} else {
		log.Error(Red("Unknown type of frame, dropping connection"))
		return nil, errors.New("Not found in matching")
	}
	return clientData, err
}

type serverUDPListener struct {
	s            *Server
	pc           *net.UDPConn
	streamType   StreamType
	writeTimeout time.Duration
	readBuf      *multibuffer.MultiBuffer
	clientsMutex sync.RWMutex
	clients      map[clientAddr]*clientData
	ringBuffer   *ringbuffer.RingBuffer

	// out
	done chan struct{}
}

func newServerUDPListener(
	s *Server,
	address string,
	streamType StreamType) (*serverUDPListener, error) {

	tmp, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, err
	}
	pc := tmp.(*net.UDPConn)

	err = pc.SetReadBuffer(serverConnUDPListenerKernelReadBufferSize)
	if err != nil {
		return nil, err
	}

	u := &serverUDPListener{
		s:       s,
		pc:      pc,
		clients: make(map[clientAddr]*clientData),
		done:    make(chan struct{}),
	}

	u.streamType = streamType
	u.writeTimeout = s.WriteTimeout
	u.readBuf = multibuffer.New(uint64(s.ReadBufferCount), uint64(s.ReadBufferSize))
	u.ringBuffer = ringbuffer.New(uint64(s.ReadBufferCount))

	go u.run()

	return u, nil
}

func (u *serverUDPListener) close() {
	u.pc.Close()
	u.ringBuffer.Close()
	<-u.done
}

func (u *serverUDPListener) run() {
	defer close(u.done)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			buf := u.readBuf.Next()
			n, addr, err := u.pc.ReadFromUDP(buf)
			if err != nil {
				break
			}

			func() {
				u.clientsMutex.RLock()
				defer u.clientsMutex.RUnlock()

				var clientAddr clientAddr
				clientAddr.fill(addr.IP, addr.Port)
				clientData, ok := u.clients[clientAddr]
				if !ok {
					clientData, err = s.handleNat(n, addr, buf)
					if err != nil {
						return
					}
				}

				now := time.Now()
				if clientData.isPublishing {
					atomic.StoreInt64(clientData.sc.announcedTracks[clientData.trackID].udpLastFrameTime, now.Unix())
					clientData.sc.announcedTracks[clientData.trackID].rtcpReceiver.ProcessFrame(now, s.streamType, buf[:n])
				}

				if h, ok := u.s.Handler.(ServerHandlerOnFrame); ok {
					h.OnFrame(&ServerHandlerOnFrameCtx{
						Session:    clientData.ss,
						TrackID:    clientData.trackID,
						StreamType: u.streamType,
						Payload:    buf[:n],
					})
				}
			}()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			tmp, ok := s.ringBuffer.Pull()
			if !ok {
				return
			}
			pair := tmp.(bufAddrPair)

			s.pc.SetWriteDeadline(time.Now().Add(s.writeTimeout))
			s.pc.WriteTo(pair.buf, pair.addr)
		}
	}()

	wg.Wait()
}

func (s *serverUDPListener) port() int {
	return s.pc.LocalAddr().(*net.UDPAddr).Port
}

func (s *serverUDPListener) write(buf []byte, addr *net.UDPAddr) {
	s.ringBuffer.Push(bufAddrPair{buf, addr})
}

func (s *serverUDPListener) addClient(ip net.IP, port int, sc *ServerConn, trackID int, isPublishing bool) {
	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	var addr clientAddr
	addr.fill(ip, port)

	s.clients[addr] = &clientData{
		sc:           sc,
		trackID:      trackID,
		isPublishing: isPublishing,
	}
}

func (s *serverUDPListener) removeClient(ip net.IP, port int) {
	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	var addr clientAddr
	addr.fill(ip, port)

	delete(s.clients, addr)
}
