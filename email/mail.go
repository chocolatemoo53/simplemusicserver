package email

import (
	"fmt"

	"github.com/resend/resend-go/v2"
)

type ResendMailer struct {
	client *resend.Client
}

var mailer *ResendMailer

func Init(apiKey string) {
	mailer = &ResendMailer{
		client: resend.NewClient(apiKey),
	}
}

func IsEnabled() bool {
	return mailer != nil && mailer.client != nil
}

func Send(from, to, subject, content string, cc, bcc []string) error {
	if mailer == nil {
		return fmt.Errorf("mailer not initialized")
	}

	params := &resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Html:    content,
		Subject: subject,
		Cc:      cc,
		Bcc:     bcc,
	}

	sent, err := mailer.client.Emails.Send(params)
	if err != nil {
		return err
	}

	fmt.Println(sent.Id)
	return nil
}
