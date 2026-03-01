// Governing: SPEC-0015 REQ "SMTP Configuration", ADR-0026
package mailer

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
)

// Config holds SMTP configuration for the mailer.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      bool
}

// Mailer defines the interface for sending email notifications.
type Mailer interface {
	Send(to, subject, body string) error
	IsConfigured() bool
}

// SMTPMailer sends emails via SMTP.
type SMTPMailer struct {
	cfg    Config
	logger *slog.Logger
}

// NoopMailer is a no-op mailer used when SMTP is not configured.
type NoopMailer struct {
	logger *slog.Logger
}

// New returns an SMTPMailer if cfg.Host is set, otherwise a NoopMailer.
func New(cfg Config, logger *slog.Logger) Mailer {
	if cfg.Host != "" {
		return &SMTPMailer{cfg: cfg, logger: logger}
	}
	return &NoopMailer{logger: logger}
}

// Send delivers an email via SMTP with STARTTLS or implicit TLS support.
func (m *SMTPMailer) Send(to, subject, body string) error {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		m.cfg.From, to, subject, body)

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	if m.cfg.TLS {
		// Use STARTTLS (port 587) via smtp.SendMail which handles STARTTLS automatically
		tlsConfig := &tls.Config{
			ServerName: m.cfg.Host,
			MinVersion: tls.VersionTLS12,
		}

		// For port 465 (implicit TLS), dial with TLS directly
		if m.cfg.Port == 465 {
			conn, err := tls.Dial("tcp", addr, tlsConfig)
			if err != nil {
				return fmt.Errorf("tls dial: %w", err)
			}
			defer conn.Close()

			c, err := smtp.NewClient(conn, m.cfg.Host)
			if err != nil {
				return fmt.Errorf("smtp client: %w", err)
			}
			defer c.Close()

			if auth != nil {
				if err := c.Auth(auth); err != nil {
					return fmt.Errorf("smtp auth: %w", err)
				}
			}
			if err := c.Mail(m.cfg.From); err != nil {
				return fmt.Errorf("smtp mail: %w", err)
			}
			if err := c.Rcpt(to); err != nil {
				return fmt.Errorf("smtp rcpt: %w", err)
			}
			w, err := c.Data()
			if err != nil {
				return fmt.Errorf("smtp data: %w", err)
			}
			if _, err := w.Write([]byte(msg)); err != nil {
				return fmt.Errorf("smtp write: %w", err)
			}
			if err := w.Close(); err != nil {
				return fmt.Errorf("smtp close data: %w", err)
			}
			return c.Quit()
		}

		// Port 587: smtp.SendMail handles STARTTLS
		return smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg))
	}

	// No TLS
	return smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg))
}

// IsConfigured returns true since SMTPMailer is only created when SMTP is configured.
func (m *SMTPMailer) IsConfigured() bool {
	return true
}

// Send logs the email at debug level and returns nil.
func (m *NoopMailer) Send(to, subject, body string) error {
	m.logger.Debug("noop mailer: email not sent (SMTP not configured)",
		"to", to,
		"subject", subject)
	return nil
}

// IsConfigured returns false since NoopMailer indicates SMTP is not configured.
func (m *NoopMailer) IsConfigured() bool {
	return false
}
