package adb

// goadb_server_test.go — the REAL-wire proof for the adb session recorder (B12:
// the changed runtime path must execute over a live wire, mirroring plugin-vnc's
// RFC 6143 RFB server stub). This file implements a MINIMAL goadb SERVER over a
// real TCP socket — enough of the adb server wire protocol for the recorder's
// device ops: host:transport selection, raw shell (start/stop invocation), the
// sync STAT (finalize detection) and sync RECV (the GetFile pull). The recorder
// dials the stub exactly like it dials the emulator's published adb server
// (adbDeviceForAddr → goadb wire) and runs the whole bracket: shell start,
// shell SIGINT, STAT finalize-wait, RECV pull, evidence finalize. Proven live:
// the goadb wire path cannot be exercised by the fakeDevice tests.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubState is the goadb server stub's scripted device: the shell output the
// recorder's commands "produce" and the sync STAT sizes the recording reports
// (growing while streaming, then stable = finalized). Thread-safe: every device
// op arrives on a FRESH connection (goadb dials per round trip), so the STAT
// counter is shared across connections (atomic).
type stubState struct {
	shellOut   string
	statSizes  []int32 // scripted STAT replies, consumed in order
	statIdx    int64
	mp4Bytes   string // the canned device file RECV streams
	shellCalls []string
	shellMu    chan int
}

func (s *stubState) nextStatSize() int32 {
	i := atomic.AddInt64(&s.statIdx, 1) - 1
	if int(i) >= len(s.statSizes) {
		return s.statSizes[len(s.statSizes)-1]
	}
	return s.statSizes[i]
}

func (s *stubState) recordShell(cmd string) {
	s.shellMu <- 0
	s.shellCalls = append(s.shellCalls, cmd)
	<-s.shellMu
}

func (s *stubState) shellCallsJoined() string {
	s.shellMu <- 0
	defer func() { <-s.shellMu }()
	return strings.Join(s.shellCalls, " | ")
}

// serveStub accepts connections and drives one client round trip per connection.
func serveStub(ln net.Listener, st *stubState) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			for {
				req, err := readHexMessage(c)
				if err != nil {
					return
				}
				switch {
				case strings.HasPrefix(req, "host:transport:"):
					// device selection: OKAY, then the connection is bound to the device.
					writeOKAY(c)
				case strings.HasPrefix(req, "shell:"):
					// raw shell: OKAY + canned output + EOF (the client reads till close).
					st.recordShell(strings.TrimPrefix(req, "shell:"))
					writeOKAY(c)
					io.WriteString(c, st.shellOut) //nolint:errcheck
					return
				case strings.HasPrefix(req, "sync:"):
					// sync sub-protocol on the same connection (STAT/RECV).
					writeOKAY(c)
					serveSync(c, st)
					return
				default:
					writeFail(c, "unknown service")
					return
				}
			}
		}(conn)
	}
}

// serveSync drives one sync-mode connection: STAT replies from the scripted sizes,
// RECV replies streaming the canned MP4 then DONE.
func serveSync(c net.Conn, st *stubState) {
	for {
		id := make([]byte, 4)
		if _, err := io.ReadFull(c, id); err != nil {
			return
		}
		var l int32
		if err := binary.Read(c, binary.LittleEndian, &l); err != nil {
			return
		}
		payload := make([]byte, l)
		if _, err := io.ReadFull(c, payload); err != nil {
			return
		}
		switch string(id) {
		case "STAT":
			size := st.nextStatSize()
			// reply: STAT + int32 mode + int32 size + int32 mtime.
			io.WriteString(c, "STAT") //nolint:errcheck
			_ = binary.Write(c, binary.LittleEndian, int32(0o100644))
			_ = binary.Write(c, binary.LittleEndian, size)
			_ = binary.Write(c, binary.LittleEndian, int32(time.Now().Unix()))
		case "RECV":
			data := []byte(st.mp4Bytes)
			for len(data) > 0 {
				chunk := data
				if len(chunk) > 65536 {
					chunk = chunk[:65536]
				}
				io.WriteString(c, "DATA") //nolint:errcheck
				_ = binary.Write(c, binary.LittleEndian, int32(len(chunk)))
				_, _ = c.Write(chunk)
				data = data[len(chunk):]
			}
			io.WriteString(c, "DONE") //nolint:errcheck
			_ = binary.Write(c, binary.LittleEndian, int32(0))
			return
		default:
			return
		}
	}
}

// readHexMessage reads one server-protocol message: "%04x" + payload.
func readHexMessage(r io.Reader) (string, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", err
	}
	n, err := strconv.ParseInt(string(hdr), 16, 32)
	if err != nil {
		return "", err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}

func writeOKAY(w io.Writer) { io.WriteString(w, "OKAY") } //nolint:errcheck

func writeFail(w io.Writer, msg string) {
	io.WriteString(w, "FAIL") //nolint:errcheck
	io.WriteString(w, fmt.Sprintf("%04x%s", len(msg), msg))
}

// TestRunSessionRecorderAgainstRealGoadbServer runs the recorder end to end
// against the REAL goadb wire (a minimal adb server stub on a real TCP socket):
// shell start (screenrecord launch), shell SIGINT (pkill), sync STAT finalize
// detection, sync RECV GetFile pull, and the evidence row — the goadb wire path
// the fakeDevice tests cannot reach.
func TestRunSessionRecorderAgainstRealGoadbServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	st := &stubState{
		// start gate sees the capture file immediately; the stop path sees two
		// equal nonzero sizes = finalized. shellOut carries the display-ready
		// marker (dumpsys display) — the E-4 display gate's probe reads it, so
		// the launch proceeds over the real wire.
		shellOut:  "mState=ON",
		statSizes: []int32{0, 120, 120},
		mp4Bytes:  "\x00\x00\x00\x18ftypmp42" + strings.Repeat("x", 64),
		shellMu:   make(chan int, 1),
	}
	go serveStub(ln, st)

	stateDir := t.TempDir()
	cfg := RecorderConfig{
		StateDir:     stateDir,
		SessionID:    "bed.member.cap",
		Venue:        "check-android-emulator-pod",
		Phase:        "live",
		StartBudget:  2 * time.Second,
		FinalizeWait: 2 * time.Second,
	}
	// Dial the stub over the REAL goadb wire — the exact dial the recorder uses
	// (adbDeviceForAddr): the whole bracket below then exercises device.RunCommand
	// (shell start/stop), device.Stat (sync STAT) and device.OpenRead (sync RECV)
	// against a real TCP socket.
	dev, err := adbDeviceForAddr(ln.Addr().String(), "emulator-5554")
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}
	done := make(chan struct{})
	type res struct {
		n   int64
		err error
	}
	rc := make(chan res, 1)
	go func() {
		n, err := runSessionCapture(dev, cfg, done)
		rc <- res{n, err}
	}()
	// Let the start gate + shell launch complete, then close done (the SIGTERM analog).
	time.Sleep(150 * time.Millisecond)
	close(done)
	got := <-rc
	if got.err != nil {
		t.Fatalf("recorder over real goadb wire: %v", got.err)
	}
	if got.n != int64(len(st.mp4Bytes)) {
		t.Errorf("pulled %d bytes, want %d", got.n, len(st.mp4Bytes))
	}
	// the pulled MP4 + evidence row landed in the state dir.
	mp4 := filepath.Join(stateDir, "bed.member.cap.mp4")
	b, err := os.ReadFile(mp4)
	if err != nil {
		t.Fatalf("session mp4 missing after stop: %v", err)
	}
	if string(b) != st.mp4Bytes {
		t.Errorf("pulled content mismatch (GetFile via sync RECV)")
	}
	if _, err := os.Stat(filepath.Join(stateDir, finalMarker)); err != nil {
		t.Fatalf("FINAL marker missing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json missing: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if row.Verb != "adb" || len(row.Artifact) != 1 || row.Artifact[0].Kind != "mp4" {
		t.Errorf("row.json = %+v, want the adb session row with the mp4 artifact", row)
	}
	// the bracket ran over the real wire: the shell start + the SIGINT stop.
	// (goadb's prepareCommandLine quotes the whitespace-bearing -c argument, so
	// the wire carries `sh -c "nohup screenrecord … &"` — match the payload.)
	calls := st.shellCallsJoined()
	if !strings.Contains(calls, "nohup screenrecord --time-limit 1800 /sdcard/bed.member.cap.mp4 >/dev/null 2>&1 &") ||
		!strings.Contains(calls, "pkill -INT screenrecord") {
		t.Errorf("shell calls = %q, want the start + SIGINT stop brackets", calls)
	}
}
