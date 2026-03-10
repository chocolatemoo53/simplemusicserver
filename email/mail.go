package email

import (
	"fmt"
	
	"github.com/resend/resend-go/v2"
)

type ResendMailer struct {
	client *resend.Client
}

func NewResendMailer(apiKey string) *ResendMailer {
	return &ResendMailer{
		client: resend.NewClient(apiKey),
	}
}

func (r *ResendMailer) Send(from, to, subject, content string, cc, bcc []string) error {
	params := &resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Html:    content,
		Subject: subject,
		Cc:      cc,
		Bcc:     bcc,
	}

	sent, err := r.client.Emails.Send(params)
	if err != nil {
		return err
	}
	fmt.Println(sent.Id) // Print the sent email ID
	return nil
}

func (r *ResendMailer) IsEnabled() bool {
	return r.client != nil
}

// Example usage
// func main() {
// 	apiKey := "re_123456789"
// 	resendMailer := NewResendMailer(apiKey)

// 	if resendMailer.IsEnabled() {
// 		err := resendMailer.Send("Acme <onboarding@resend.dev>", "delivered@resend.dev", "Hello from Golang", "<strong>hello world</strong>", []string{"cc@example.com"}, []string{"bcc@example.com"})
// 		if err != nil {
// 			log.Println("Resend email sending failed:", err)
// 		}
// 	}
// }
