package headers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aler9/gortsplib/pkg/base"
)

// TransportMode is a transport mode.
type TransportMode int

const (
	// TransportModePlay is the "play" transport mode
	TransportModePlay TransportMode = iota

	// TransportModeRecord is the "record" transport mode
	TransportModeRecord
)

// String implements fmt.Stringer.
func (tm TransportMode) String() string {
	switch tm {
	case TransportModePlay:
		return "play"

	case TransportModeRecord:
		return "record"
	}
	return "unknown"
}

// Transport is a Transport header.
type Transport struct {
	// protocol of the stream
	Protocol base.StreamProtocol

	// (optional) delivery method of the stream
	Delivery *base.StreamDelivery

	// (optional) destination
	Destination *string

	// (optional) TTL
	TTL *uint

	// (optional) ports
	Ports *[2]int

	// (optional) client ports
	ClientPorts *[2]int

	// (optional) server ports
	ServerPorts *[2]int

	// (optional) interleaved frame ids
	InterleavedIDs *[2]int

	// (optional) mode
	Mode *TransportMode
}

func parsePorts(val string) (*[2]int, error) {
	ports := strings.Split(val, "-")
	if len(ports) == 2 {
		port1, err := strconv.ParseInt(ports[0], 10, 64)
		if err != nil {
			return &[2]int{0, 0}, fmt.Errorf("invalid ports (%v)", val)
		}

		port2, err := strconv.ParseInt(ports[1], 10, 64)
		if err != nil {
			return &[2]int{0, 0}, fmt.Errorf("invalid ports (%v)", val)
		}

		return &[2]int{int(port1), int(port2)}, nil
	}

	if len(ports) == 1 {
		port1, err := strconv.ParseInt(ports[0], 10, 64)
		if err != nil {
			return &[2]int{0, 0}, fmt.Errorf("invalid ports (%v)", val)
		}

		return &[2]int{int(port1), int(port1 + 1)}, nil
	}

	return &[2]int{0, 0}, fmt.Errorf("invalid ports (%v)", val)
}

// Read decodes a Transport header.
func (h *Transport) Read(v base.HeaderValue) error {
	if len(v) == 0 {
		return fmt.Errorf("value not provided")
	}

	if len(v) > 1 {
		return fmt.Errorf("value provided multiple times (%v)", v)
	}

	v0 := v[0]

	var part string
	i := strings.IndexByte(v0, ';')
	if i >= 0 {
		part, v0 = v0[:i], v0[i+1:]
	} else {
		part, v0 = v0, ""
	}

	switch part {
	case "RTP/AVP", "RTP/AVP/UDP":
		h.Protocol = base.StreamProtocolUDP

	case "RTP/AVP/TCP":
		h.Protocol = base.StreamProtocolTCP

	default:
		return fmt.Errorf("invalid protocol (%v)", v)
	}

	i = strings.IndexByte(v0, ';')
	if i >= 0 {
		part, v0 = v0[:i], v0[i+1:]
	} else {
		part, v0 = v0, ""
	}

	switch part {
	case "unicast":
		v := base.StreamDeliveryUnicast
		h.Delivery = &v

	case "multicast":
		v := base.StreamDeliveryMulticast
		h.Delivery = &v

	default:
		// cast is optional, go back
		v0 = part + ";" + v0
	}

	kvs, err := keyValParse(v0, ';')
	if err != nil {
		return err
	}

	for k, rv := range kvs {
		v := rv

		switch k {
		case "destination":
			h.Destination = &v

		case "ttl":
			tmp, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return err
			}
			vu := uint(tmp)
			h.TTL = &vu

		case "port":
			ports, err := parsePorts(v)
			if err != nil {
				return err
			}
			h.Ports = ports

		case "client_port":
			ports, err := parsePorts(v)
			if err != nil {
				return err
			}
			h.ClientPorts = ports

		case "server_port":
			ports, err := parsePorts(v)
			if err != nil {
				return err
			}
			h.ServerPorts = ports

		case "interleaved":
			ports, err := parsePorts(v)
			if err != nil {
				return err
			}
			h.InterleavedIDs = ports

		case "mode":
			str := strings.ToLower(v)
			str = strings.TrimPrefix(str, "\"")
			str = strings.TrimSuffix(str, "\"")

			switch str {
			case "play":
				v := TransportModePlay
				h.Mode = &v

				// receive is an old alias for record, used by ffmpeg with the
				// -listen flag, and by Darwin Streaming Server
			case "record", "receive":
				v := TransportModeRecord
				h.Mode = &v

			default:
				return fmt.Errorf("invalid transport mode: '%s'", str)
			}

		default:
			// ignore non-standard keys
		}
	}

	return nil
}

// Write encodes a Transport header
func (h Transport) Write() base.HeaderValue {
	var rets []string

	if h.Protocol == base.StreamProtocolUDP {
		rets = append(rets, "RTP/AVP")
	} else {
		rets = append(rets, "RTP/AVP/TCP")
	}

	if h.Delivery != nil {
		if *h.Delivery == base.StreamDeliveryUnicast {
			rets = append(rets, "unicast")
		} else {
			rets = append(rets, "multicast")
		}
	}

	if h.ClientPorts != nil {
		ports := *h.ClientPorts
		rets = append(rets, "client_port="+strconv.FormatInt(int64(ports[0]), 10)+"-"+strconv.FormatInt(int64(ports[1]), 10))
	}

	if h.ServerPorts != nil {
		ports := *h.ServerPorts
		rets = append(rets, "server_port="+strconv.FormatInt(int64(ports[0]), 10)+"-"+strconv.FormatInt(int64(ports[1]), 10))
	}

	if h.InterleavedIDs != nil {
		ports := *h.InterleavedIDs
		rets = append(rets, "interleaved="+strconv.FormatInt(int64(ports[0]), 10)+"-"+strconv.FormatInt(int64(ports[1]), 10))
	}

	if h.Mode != nil {
		if *h.Mode == TransportModePlay {
			rets = append(rets, "mode=play")
		} else {
			rets = append(rets, "mode=record")
		}
	}

	return base.HeaderValue{strings.Join(rets, ";")}
}
