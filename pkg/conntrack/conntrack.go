package conntrack

import (
	"errors"
	ct "github.com/florianl/go-conntrack"
	log "github.com/sirupsen/logrus"
)

func ConntrackFindRealSourcePort(sPort int) (int, error) {
	nfct, err := ct.Open(&ct.Config{})
	if err != nil {
		log.Errorln("could not create nfct:", err)
		return -1, err
	}
	defer nfct.Close()

	// Get all IPv4 entries of the expected table.
	sessions, err := nfct.Dump(ct.Conntrack, ct.IPv4)
	if err != nil {
		log.Errorln("could not dump sessions:", err)
		return -1, err
	}

	// Print out all expected sessions.
	for _, sessionInside := range sessions {
		if *sessionInside.Origin.Proto.SrcPort == (uint16)(sPort) {
      log.Debugf("Ins: osp %v  odp %v rsp %v rdp %v\n", *sessionInside.Origin.Proto.SrcPort, *sessionInside.Origin.Proto.DstPort, *sessionInside.Reply.Proto.SrcPort, *sessionInside.Reply.Proto.DstPort)
      dPort := *sessionInside.Origin.Proto.DstPort
			for _, sessionOutside := range sessions {
				if *sessionOutside.Origin.Proto.SrcPort == (uint16)(dPort) {
          log.Debugf("Outs: osp %v  odp %v rsp %v rdp %v\n", *sessionOutside.Origin.Proto.SrcPort, *sessionOutside.Origin.Proto.DstPort, *sessionOutside.Reply.Proto.SrcPort, *sessionOutside.Reply.Proto.DstPort)
					return (int)(*sessionOutside.Origin.Proto.DstPort), nil
				}
			}
		}
	}
	return -1, errors.New("Not found!")
}
