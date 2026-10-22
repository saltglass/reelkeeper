package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// newsServer is a read-only news server with just the NNTP (RFC 3977)
// commands a Usenet download client uses: greet, log in, pick a group,
// check an article and fetch one. Articles are found by message-id only.
type newsServer struct {
	data string
	// down makes it refuse new connections and drop open ones, for "the
	// provider went away mid-download".
	down atomic.Bool
	log  *slog.Logger
}

func (s *newsServer) serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *newsServer) handle(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	reply := func(format string, args ...any) bool {
		fmt.Fprintf(w, format+"\r\n", args...)
		return w.Flush() == nil
	}
	if s.down.Load() {
		return // no greeting: the connection just drops, as an unreachable server's does
	}
	if !reply("200 fakenews ready (posting prohibited)") {
		return
	}
	r := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if s.down.Load() {
			return // the connection just drops, as a dead server's does
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		cmd, args := strings.ToUpper(fields[0]), fields[1:]
		switch cmd {
		case "CAPABILITIES":
			reply("101 capability list follows\r\nVERSION 2\r\nREADER\r\nAUTHINFO USER\r\n.")
		case "MODE":
			reply("201 reader mode, posting prohibited")
		case "AUTHINFO":
			// Any login works; the dev clients use a throwaway one.
			if len(args) > 0 && strings.EqualFold(args[0], "USER") {
				reply("381 password required")
			} else {
				reply("281 authentication accepted")
			}
		case "GROUP":
			if len(args) == 0 {
				reply("501 no group given")
			} else {
				reply("211 0 0 0 %s", args[0])
			}
		case "DATE":
			reply("111 %s", time.Now().UTC().Format("20060102150405"))
		case "STAT", "HEAD", "BODY", "ARTICLE":
			if len(args) == 0 || !strings.HasPrefix(args[0], "<") {
				reply("420 only lookups by message-id are supported")
				continue
			}
			id := strings.Trim(args[0], "<>")
			body, err := os.ReadFile(articlePath(s.data, id))
			if errors.Is(err, os.ErrNotExist) {
				s.log.Info("article missing", "id", id)
				reply("430 no such article")
				continue
			} else if err != nil {
				reply("403 %v", err)
				continue
			}
			head := fmt.Sprintf("Message-ID: <%s>\r\nNewsgroups: %s\r\nFrom: fakenews <fakenews@reelkeeper.test>\r\nSubject: %s\r\n", id, group, id)
			switch cmd {
			case "STAT":
				reply("223 0 <%s>", id)
			case "HEAD":
				reply("221 0 <%s>", id)
				writeBlock(w, head)
			case "BODY":
				reply("222 0 <%s>", id)
				writeBlock(w, string(body))
			case "ARTICLE":
				reply("220 0 <%s>", id)
				writeBlock(w, head+"\r\n"+string(body))
			}
			if w.Flush() != nil {
				return
			}
		case "QUIT":
			reply("205 bye")
			return
		default:
			reply("500 unknown command")
		}
	}
}

// writeBlock sends a multi-line answer: each line dot-stuffed (a line
// starting with "." gets another), then a line with a single ".".
func writeBlock(w io.Writer, text string) {
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		io.WriteString(w, line+"\r\n")
	}
	io.WriteString(w, ".\r\n")
}
