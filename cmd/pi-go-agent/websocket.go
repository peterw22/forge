package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	webSocketGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxWebSocketMessage = 32 * 1024 * 1024
)

func serveWebSocket(registry *runtimeRegistry, endpoint string, allowRemote bool) error {
	address, path, err := parseWebSocketEndpoint(endpoint, allowRemote)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", endpoint, err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true}`+"\n")
	})
	mux.HandleFunc(path, func(writer http.ResponseWriter, request *http.Request) {
		stream, upgradeErr := upgradeWebSocket(writer, request)
		if upgradeErr != nil {
			http.Error(writer, upgradeErr.Error(), http.StatusBadRequest)
			return
		}
		defer stream.Close()
		if serveErr := serveClient(registry, stream, stream, stream); serveErr != nil && !errors.Is(serveErr, errBackendShutdown) {
			fmt.Fprintln(os.Stderr, "websocket client disconnected:", serveErr)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-registry.done
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	// Stderr is used so stdout remains free of accidental protocol output when
	// the backend is launched by another process.
	fmt.Fprintf(os.Stderr, "pi-go-agent websocket listening on ws://%s%s\n", listener.Addr(), path)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func parseWebSocketEndpoint(endpoint string, allowRemote bool) (address, path string, err error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "ws" || parsed.Host == "" {
		return "", "", errors.New("WebSocket listen address must be ws://loopback:port/path")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !allowRemote && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", "", errors.New("WebSocket listener must use a loopback address unless --allow-remote is set")
	}
	if parsed.Port() == "" {
		return "", "", errors.New("WebSocket listener port is required")
	}
	path = parsed.EscapedPath()
	if path == "" || path == "/" {
		path = "/ws"
	}
	return parsed.Host, path, nil
}

func upgradeWebSocket(writer http.ResponseWriter, request *http.Request) (*webSocketStream, error) {
	if request.Method != http.MethodGet || !headerToken(request.Header, "Upgrade", "websocket") || !headerToken(request.Header, "Connection", "upgrade") {
		return nil, errors.New("WebSocket upgrade required")
	}
	if !allowedWebSocketOrigin(request.Header.Get("Origin")) {
		return nil, errors.New("WebSocket origin must be loopback")
	}
	key := strings.TrimSpace(request.Header.Get("Sec-WebSocket-Key"))
	version := request.Header.Get("Sec-WebSocket-Version")
	if key == "" || version != "13" {
		return nil, errors.New("invalid WebSocket handshake")
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		return nil, errors.New("HTTP server does not support WebSocket hijacking")
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	digest := sha1.Sum([]byte(key + webSocketGUID))
	accept := base64.StdEncoding.EncodeToString(digest[:])
	_, err = fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err == nil {
		err = buffered.Flush()
	}
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return &webSocketStream{connection: connection, reader: buffered.Reader}, nil
}

func headerToken(header http.Header, name, wanted string) bool {
	for _, part := range strings.Split(header.Get(name), ",") {
		if strings.EqualFold(strings.TrimSpace(part), wanted) {
			return true
		}
	}
	return false
}

func allowedWebSocketOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

type webSocketStream struct {
	connection  net.Conn
	reader      *bufio.Reader
	readBuffer  []byte
	writeBuffer []byte
	writeMu     sync.Mutex
}

func (stream *webSocketStream) Read(destination []byte) (int, error) {
	for len(stream.readBuffer) == 0 {
		payload, err := stream.readMessage()
		if err != nil {
			return 0, err
		}
		stream.readBuffer = append(payload, '\n')
	}
	count := copy(destination, stream.readBuffer)
	stream.readBuffer = stream.readBuffer[count:]
	return count, nil
}

func (stream *webSocketStream) Write(value []byte) (int, error) {
	stream.writeMu.Lock()
	defer stream.writeMu.Unlock()
	stream.writeBuffer = append(stream.writeBuffer, value...)
	for {
		newline := bytes.IndexByte(stream.writeBuffer, '\n')
		if newline < 0 {
			break
		}
		message := append([]byte(nil), stream.writeBuffer[:newline]...)
		stream.writeBuffer = stream.writeBuffer[newline+1:]
		if err := stream.writeFrameLocked(0x1, message); err != nil {
			return 0, err
		}
	}
	return len(value), nil
}

func (stream *webSocketStream) Close() error {
	stream.writeMu.Lock()
	_ = stream.writeFrameLocked(0x8, nil)
	stream.writeMu.Unlock()
	return stream.connection.Close()
}

func (stream *webSocketStream) readMessage() ([]byte, error) {
	var message []byte
	started := false
	for {
		fin, opcode, payload, err := stream.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			stream.writeMu.Lock()
			err = stream.writeFrameLocked(0xA, payload)
			stream.writeMu.Unlock()
			if err != nil {
				return nil, err
			}
			continue
		case 0xA:
			continue
		case 0x1:
			if started {
				return nil, errors.New("unexpected WebSocket text frame")
			}
			started = true
		case 0x0:
			if !started {
				return nil, errors.New("unexpected WebSocket continuation")
			}
		default:
			return nil, errors.New("only WebSocket text frames are supported")
		}
		message = append(message, payload...)
		if len(message) > maxWebSocketMessage {
			return nil, errors.New("WebSocket message is too large")
		}
		if fin {
			return message, nil
		}
	}
}

func (stream *webSocketStream) readFrame() (bool, byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(stream.reader, header[:]); err != nil {
		return false, 0, nil, err
	}
	fin, opcode, masked := header[0]&0x80 != 0, header[0]&0x0f, header[1]&0x80 != 0
	if !masked {
		return false, 0, nil, errors.New("client WebSocket frames must be masked")
	}
	length := uint64(header[1] & 0x7f)
	if length == 126 {
		var extended [2]byte
		if _, err := io.ReadFull(stream.reader, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	} else if length == 127 {
		var extended [8]byte
		if _, err := io.ReadFull(stream.reader, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if length > maxWebSocketMessage {
		return false, 0, nil, errors.New("WebSocket frame is too large")
	}
	var mask [4]byte
	if _, err := io.ReadFull(stream.reader, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(stream.reader, payload); err != nil {
		return false, 0, nil, err
	}
	for index := range payload {
		payload[index] ^= mask[index%4]
	}
	return fin, opcode, payload, nil
}

func (stream *webSocketStream) writeFrameLocked(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch length := len(payload); {
	case length < 126:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	if _, err := stream.connection.Write(header); err != nil {
		return err
	}
	_, err := stream.connection.Write(payload)
	return err
}
