package email

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"

	"payment-gateway/internal/config"
)

type Service struct {
	cfg *config.Config
}

type Message struct {
	To      string
	Subject string
	Body    string
}

func NewService(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Enabled() bool {
	return s.cfg.SMTPHost != "" && s.cfg.SMTPPort > 0 && s.cfg.SMTPFromEmail != ""
}

func (s *Service) Send(msg Message) error {
	if !s.Enabled() {
		slog.Info("Email desabilitado: SMTP não configurado", "subject", msg.Subject)
		return nil
	}
	if msg.To == "" {
		return fmt.Errorf("destinatário de email vazio")
	}

	fromName := strings.TrimSpace(s.cfg.SMTPFromName)
	from := s.cfg.SMTPFromEmail
	if fromName != "" {
		from = fmt.Sprintf("%s <%s>", fromName, s.cfg.SMTPFromEmail)
	}

	raw := strings.Join([]string{
		"From: " + from,
		"To: " + msg.To,
		"Subject: " + msg.Subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		msg.Body,
	}, "\r\n")

	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)
	auth := smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPass, s.cfg.SMTPHost)
	if s.cfg.SMTPSecure {
		return s.sendStartTLS(addr, auth, []byte(raw), msg.To)
	}
	return smtp.SendMail(addr, auth, s.cfg.SMTPFromEmail, []string{msg.To}, []byte(raw))
}

func (s *Service) NotifyOps(subject, body string) {
	if s.cfg.OpsEmail == "" {
		return
	}
	if err := s.Send(Message{To: s.cfg.OpsEmail, Subject: subject, Body: body}); err != nil {
		slog.Warn("Falha ao enviar email operacional", "error", err)
	}
}

func (s *Service) sendStartTLS(addr string, auth smtp.Auth, raw []byte, to string) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(s.cfg.SMTPFromEmail); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
