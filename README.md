
# gortsplib

[![Test](https://github.com/aler9/gortsplib/workflows/test/badge.svg)](https://github.com/aler9/gortsplib/actions?query=workflow:test)
[![Lint](https://github.com/aler9/gortsplib/workflows/lint/badge.svg)](https://github.com/aler9/gortsplib/actions?query=workflow:lint)
[![CodeCov](https://codecov.io/gh/aler9/gortsplib/branch/main/graph/badge.svg)](https://codecov.io/gh/aler9/gortsplib/branch/main)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/aler9/gortsplib)](https://pkg.go.dev/github.com/aler9/gortsplib#pkg-index)

RTSP 1.0 client and server library for the Go programming language, written for [rtsp-simple-server](https://github.com/aler9/rtsp-simple-server).

Go &ge; 1.14 is required.

Features:

* Client
  * Query servers about available streams
  * Encrypt connection with TLS (RTSPS)
  * Read
    * Read streams from servers with UDP or TCP
    * Switch protocol automatically (switch to TCP in case of server error or UDP timeout)
    * Read only selected tracks of a stream
    * Pause reading without disconnecting from the server
    * Generate RTCP receiver reports automatically
  * Publish
    * Publish streams to servers with UDP or TCP
    * Switch protocol automatically (switch to TCP in case of server error)
    * Pause publishing without disconnecting from the server
    * Generate RTCP sender reports automatically
* Server
  * Handle requests from clients
  * Sessions and connections are independent; clients can control multiple sessions
  * Read streams from clients with UDP or TCP
  * Write streams to clients with UDP or TCP
  * Encrypt streams with TLS (RTSPS)
  * Generate RTCP receiver reports automatically
* Utilities
  * Encode and decode RTSP primitives, RTP/H264, RTP/AAC, SDP

## Table of contents

* [Examples](#examples)
* [API Documentation](#api-documentation)
* [Links](#links)

## Examples

* [client-query](examples/client-query/main.go)
* [client-read](examples/client-read/main.go)
* [client-read-partial](examples/client-read-partial/main.go)
* [client-read-options](examples/client-read-options/main.go)
* [client-read-pause](examples/client-read-pause/main.go)
* [client-read-h264](examples/client-read-h264/main.go)
* [client-publish](examples/client-publish/main.go)
* [client-publish-options](examples/client-publish-options/main.go)
* [client-publish-pause](examples/client-publish-pause/main.go)
* [server](examples/server/main.go)
* [server-tls](examples/server-tls/main.go)

## API Documentation

https://pkg.go.dev/github.com/aler9/gortsplib#pkg-index

## Links

Related projects

* https://github.com/aler9/rtsp-simple-server
* https://github.com/pion/sdp (SDP library used internally)
* https://github.com/pion/rtcp (RTCP library used internally)
* https://github.com/pion/rtp (RTP library used internally)

IETF Standards

* RTSP 1.0 https://tools.ietf.org/html/rfc2326
* RTSP 2.0 https://tools.ietf.org/html/rfc7826
* HTTP 1.1 https://tools.ietf.org/html/rfc2616

Conventions

* https://github.com/golang-standards/project-layout
