package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type Sender interface {
	Send(to, subject, text string) error
}
type SMTP struct {
	Host, Username, Password, From string
	Port                           int
}

func (s SMTP) Send(to, subject, text string) error {
	if s.Host == "" || s.From == "" || s.Port < 1 || s.Port > 65535 {
		return errors.New("邮件服务尚未配置")
	}
	if strings.ContainsAny(s.Host+s.From+to+subject, "\r\n") || strings.ContainsAny(s.Host, "/@") {
		return errors.New("邮件配置无效")
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return errors.New("发件人地址无效")
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return errors.New("收件人地址无效")
	}
	address := net.JoinHostPort(s.Host, fmt.Sprint(s.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if s.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", address)
	}
	if err != nil {
		return errors.New("无法连接邮件服务")
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return errors.New("邮件服务握手失败")
	}
	defer client.Close()
	if s.Port != 465 {
		if available, _ := client.Extension("STARTTLS"); !available {
			return errors.New("邮件服务必须支持STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("邮件服务TLS失败")
		}
	}
	if s.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return errors.New("邮件认证失败")
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return errors.New("邮件发送失败")
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return errors.New("邮件收件人被拒绝")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("邮件发送失败")
	}
	message := "To: " + recipient.String() + "\r\nFrom: " + from.String() + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n") + "\r\n"
	if _, err = writer.Write([]byte(message)); err != nil {
		return errors.New("邮件写入失败")
	}
	if err = writer.Close(); err != nil {
		return errors.New("邮件发送失败")
	}
	if err = client.Quit(); err != nil {
		return errors.New("邮件确认失败")
	}
	return nil
}
