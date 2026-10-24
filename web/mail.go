package web

import (
	"github.com/guregu/intertube/email"
	"github.com/guregu/intertube/tube"
)

func SetMail(cfg email.Config) error {
	return email.Init(cfg)
}

func mailEnabled() bool {
	return email.IsEnabled()
}

func sendMail(u tube.User, subject, body string) error {
	return email.Send(email.Message{
		To:        u.Email,
		Subject:   subject,
		Body:      body,
		PublicKey: u.PGPKey,
	})
}
