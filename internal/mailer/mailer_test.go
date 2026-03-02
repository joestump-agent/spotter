package mailer

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
)

func TestNew_WithHost_ReturnsSMTPMailer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(Config{Host: "smtp.example.com", Port: 587}, logger)
	if _, ok := m.(*SMTPMailer); !ok {
		t.Errorf("expected *SMTPMailer, got %T", m)
	}
}

func TestNew_WithoutHost_ReturnsNoopMailer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(Config{}, logger)
	if _, ok := m.(*NoopMailer); !ok {
		t.Errorf("expected *NoopMailer, got %T", m)
	}
}

func TestSMTPMailer_IsConfigured(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(Config{Host: "smtp.example.com", Port: 587}, logger)
	if !m.IsConfigured() {
		t.Error("SMTPMailer.IsConfigured() should return true")
	}
}

func TestNoopMailer_IsConfigured(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(Config{}, logger)
	if m.IsConfigured() {
		t.Error("NoopMailer.IsConfigured() should return false")
	}
}

func TestNoopMailer_SendSucceedsSilently(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &NoopMailer{logger: logger}
	err := m.Send("user@example.com", "Test Subject", "Test body")
	if err != nil {
		t.Errorf("NoopMailer.Send() should return nil, got %v", err)
	}
}

func TestSMTPMailer_Send_ConnectionRefused(t *testing.T) {
	// Use a port that is not listening to trigger a connection error
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &SMTPMailer{
		cfg: Config{
			Host: "127.0.0.1",
			Port: findFreePort(t),
			From: "test@example.com",
		},
		logger: logger,
	}

	err := m.Send("user@example.com", "Test Subject", "Test body")
	if err == nil {
		t.Fatal("expected error when SMTP server is unreachable")
	}
}

func TestSMTPMailer_Send_TLS465_ConnectionRefused(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &SMTPMailer{
		cfg: Config{
			Host: "127.0.0.1",
			Port: 465,
			From: "test@example.com",
			TLS:  true,
		},
		logger: logger,
	}

	err := m.Send("user@example.com", "Test Subject", "Test body")
	if err == nil {
		t.Fatal("expected error when TLS SMTP server is unreachable")
	}
}

func TestBuildMessage_ContainsMIME(t *testing.T) {
	msg := buildMessage("from@test.com", "to@test.com", "Test Subject", "Hello body")

	checks := []string{
		"From: from@test.com",
		"To: to@test.com",
		"Subject: Test Subject",
		"MIME-Version: 1.0",
		"multipart/alternative",
		"text/plain",
		"text/html",
		"Hello body",
	}

	for _, check := range checks {
		if !strings.Contains(msg, check) {
			t.Errorf("message missing %q", check)
		}
	}
}

func TestBuildMessage_HTMLEscapesBody(t *testing.T) {
	msg := buildMessage("from@test.com", "to@test.com", "Subject", "<script>alert('xss')</script>")
	// The HTML part should contain the escaped version
	if !strings.Contains(msg, "&lt;script&gt;") {
		t.Error("HTML part should contain escaped script tags")
	}
	// The plaintext part will contain the raw string, which is expected and safe for text/plain.
	// Verify that the HTML wrapping uses <pre> with escaped content.
	if !strings.Contains(msg, "<pre>&lt;script&gt;") {
		t.Error("HTML part should wrap escaped content in <pre> tags")
	}
}

// findFreePort returns a port that is currently free (used then immediately released)
func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
