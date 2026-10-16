package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
)

func subjectFor(ev Event) string {
	if ev.Title != "" {
		return "Reelkeeper: " + ev.Title
	}
	return "Reelkeeper: " + ev.Kind
}

func bodyFor(ev Event) string {
	msg := ev.Message
	if msg == "" {
		msg = ev.Title
	}
	return msg + "\n\nSent by Reelkeeper"
}

// oneLine keeps a value from adding mail headers.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// buildMessage makes a plain-text email.
func buildMessage(from, to, subject, body string, at string) []byte {
	var b strings.Builder
	b.WriteString("From: " + oneLine(from) + "\r\n")
	b.WriteString("To: " + oneLine(to) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", oneLine(subject)) + "\r\n")
	if at != "" {
		b.WriteString("Date: " + at + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// sendEmail sends one email: implicit TLS on port 465, STARTTLS otherwise.
// It refuses to send a login over a connection without TLS, unless the
// mail server is on this machine.
func sendEmail(ctx context.Context, c settings.EmailNotify, subject, body string) error {
	if c.Host == "" || c.From == "" || c.To == "" {
		return errors.New("enter the mail server, From and To addresses first")
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return errors.New("the From address isn't valid")
	}
	to, err := mail.ParseAddress(c.To)
	if err != nil {
		return errors.New("the To address isn't valid")
	}
	port := c.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("email: can't connect to %s: %w", addr, cleanErr(err))
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	secure := false
	if port == 465 {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("email: secure connection to %s failed: %w", addr, err)
		}
		conn, secure = tc, true
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("email: %s didn't greet us properly: %w", addr, err)
	}
	defer cl.Close()

	if !secure {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("email: couldn't switch to a secure connection: %w", err)
			}
			secure = true
		}
	}
	if c.Username != "" {
		if !secure && !isLocal(c.Host) {
			return errors.New("email: the server doesn't offer a secure connection, so Reelkeeper won't send your password to it. Try port 587 or 465")
		}
		if err := cl.Auth(plainAuth{c.Username, c.Password}); err != nil {
			return fmt.Errorf("email: the server refused the login: %w", err)
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return fmt.Errorf("email: the server refused the sender: %w", err)
	}
	if err := cl.Rcpt(to.Address); err != nil {
		return fmt.Errorf("email: the server refused the recipient: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if _, err := w.Write(buildMessage(c.From, c.To, subject, body, time.Now().Format(time.RFC1123Z))); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: the server didn't accept the message: %w", err)
	}
	_ = cl.Quit()
	return nil
}

// plainAuth is SMTP PLAIN. (net/smtp's own refuses non-TLS connections to
// anything but localhost by looking at the connection; we've already made
// that decision above, including for local servers on other names.)
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}
