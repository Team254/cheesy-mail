package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
	"log"
	"net/mail"
	"os"
)

var awsSession *session.Session

// Returns the list of all addresses to distribute the message to, given the list addresses the original
// message was sent to.
func getRecipients(lists []*mail.Address) []string {
	return []string{"patfair+listtest@gmail.com"}
}

func getSubject(message *MailMessage) string {
	return "[Team 254] " + message.subject
}

func createEmail(message *MailMessage, recipient string) *ses.SendEmailInput {
	return &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("%s <%s>", message.from.Name, "listadmin@team254.com")),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(recipient)},
		},
		ReplyToAddresses: []*string{aws.String(message.from.Address)},
		Message: &ses.Message{
			Subject: &ses.Content{
				Data: aws.String(getSubject(message)),
			},
			Body: &ses.Body{
				Html: &ses.Content{
					Data: aws.String(message.body.HTML),
				},
			},
		},
	}
}

func handleMessage(message *MailMessage) {
	log.Println()
	log.Println("Message received:")
	log.Printf("From: %v", message.from)
	log.Printf("To: %v", message.to)
	log.Printf("Subject: %s", message.subject)
	log.Printf("Body: %s", message.body.HTML)

	recipients := getRecipients(message.to)
	log.Printf("Redistributing message to %d recipients: %v", len(recipients), recipients)
	service := ses.New(awsSession)
	for _, recipient := range recipients {
		email := createEmail(message, recipient)
		_, err := service.SendEmail(email)
		if err != nil {
			log.Printf("Error sending message to %s: %v", recipient, err)
		}
	}
}

func main() {
	logfile, err := os.OpenFile("cheesy-mail.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}
	log.SetOutput(logfile)

	configure()
	go runSmtpServer()
	log.Println("Listening for incoming mail.")

	// Configure AWS client session.
	config := aws.Config{Region: aws.String("us-west-2"), Credentials: credentials.NewStaticCredentials("AKIAI2B2ROJNJAWY4VUQ", "xbTDTgoRsbn5Ef0PtjW3xnsr8lXEMmdrKIMmELC3", "")}
	awsSession = session.New(&config)

	for {
		message := <-messageReceivedChan
		handleMessage(message)
	}
}
