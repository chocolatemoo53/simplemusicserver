package email

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

const (
	TLSImplicit = "tls"
	TLSStartTLS = "starttls"
	TLSNone     = "none"

	EncryptedSubject = "Encrypted message"

	dialTimeout = 30 * time.Second
	sendTimeout = 2 * time.Minute
)

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      string
}

type Message struct {
	To        string
	Subject   string
	Body      string
	PublicKey string
}

var (
	cfg     Config
	from    *mail.Address
	enabled bool
)

func Init(c Config) error {
	if c.Host == "" {
		enabled = false
		return nil
	}
	addr, err := mail.ParseAddress(c.From)
	if err != nil {
		return fmt.Errorf("mail.from must be an e-mail address like \"Music <noreply@example.com>\": %w", err)
	}
	if c.TLS == "" {
		c.TLS = TLSStartTLS
	}
	switch c.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSNone:
		if !isLoopback(c.Host) {
			return fmt.Errorf("mail.tls = \"none\" is only allowed for a mail relay on this machine")
		}
	default:
		return fmt.Errorf("mail.tls must be \"tls\", \"starttls\" or \"none\", got %q", c.TLS)
	}
	if c.Port == 0 {
		c.Port = 587
		if c.TLS == TLSImplicit {
			c.Port = 465
		}
	}
	cfg, from, enabled = c, addr, true
	return nil
}

func IsEnabled() bool {
	return enabled
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func Send(msg Message) error {
	if !enabled {
		return errors.New("mail is not configured")
	}
	to, err := mail.ParseAddress(msg.To)
	if err != nil {
		return err
	}
	data, err := Build(msg, from, time.Now())
	if err != nil {
		return err
	}
	return deliver(from.Address, to.Address, data)
}

func Build(msg Message, sender *mail.Address, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	header := func(k, v string) {
		fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
	}
	header("From", sender.String())
	header("To", msg.To)
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", messageID(sender.Address))
	header("MIME-Version", "1.0")

	if msg.PublicKey == "" {
		header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
		part, err := textPart(msg.Body)
		if err != nil {
			return nil, err
		}
		buf.Write(part)
		return buf.Bytes(), nil
	}

	inner, err := textPart(msg.Body)
	if err != nil {
		return nil, err
	}
	protected := append([]byte("Subject: "+mime.QEncoding.Encode("utf-8", msg.Subject)+"\r\n"), inner...)
	protected = bytes.Replace(protected, []byte("Content-Type: text/plain; charset=utf-8"),
		[]byte("Content-Type: text/plain; charset=utf-8; protected-headers=\"v1\""), 1)
	armored, err := Encrypt(protected, msg.PublicKey)
	if err != nil {
		return nil, err
	}

	boundary := hex.EncodeToString(randomBytes(16))
	header("Subject", EncryptedSubject)
	header("Content-Type", `multipart/encrypted; protocol="application/pgp-encrypted"; boundary="`+boundary+`"`)
	buf.WriteString("\r\n")
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: application/pgp-encrypted\r\nContent-Description: PGP/MIME version identification\r\n\r\nVersion: 1\r\n\r\n")
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: application/octet-stream; name=\"encrypted.asc\"\r\nContent-Description: OpenPGP encrypted message\r\nContent-Disposition: inline; filename=\"encrypted.asc\"\r\n\r\n")
	buf.Write(bytes.ReplaceAll(armored, []byte("\n"), []byte("\r\n")))
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)
	return buf.Bytes(), nil
}

func textPart(body string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&buf)
	if _, err := w.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	buf.WriteString("\r\n")
	return buf.Bytes(), nil
}

func messageID(address string) string {
	domain := "localhost"
	if at := strings.LastIndexByte(address, '@'); at >= 0 {
		domain = address[at+1:]
	}
	return "<" + hex.EncodeToString(randomBytes(16)) + "@" + domain + ">"
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func deliver(sender, recipient string, data []byte) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: dialTimeout}
	if cfg.TLS == TLSImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(sendTimeout))
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mail server doesn't offer STARTTLS")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(sender); err != nil {
		return err
	}
	if err := c.Rcpt(recipient); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
