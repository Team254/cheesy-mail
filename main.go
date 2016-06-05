// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Mailing list e-mail server.

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
	"strings"
)

const (
	fromAddress  = "listadmin@team254.com"
	adminAddress = "patfair+team254admin@gmail.com" // TODO(pat): update
)

var awsSession *session.Session

// Returns the list of all addresses to distribute the message to, given the list addresses the original
// message was sent to.
func getRecipients(lists []*mail.Address) ([]string, error) {
	return []string{"patfair+listtest@gmail.com"}, nil
}

func getSubject(message *MailMessage) string {
	return "[Team 254] " + message.subject
}

// Returns true if the subject line contains "DEBUG".
func isDebug(message *MailMessage) bool {
	return strings.Contains(message.subject, "DEBUG")
}

func createEmail(message *MailMessage, recipient string, allRecipients []string) *ses.SendEmailInput {
	body := message.body.HTML
	if isDebug(message) {
		// Append the list of recipients to the message.
		body += "<p><b>Debugging information</b></p><p>Recipients:<br />" +
			strings.Join(allRecipients, "<br />") + "</p>"
	}

	email := &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("%s <%s>", message.from.Name, fromAddress)),
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
					Data: aws.String(body),
				},
			},
		},
	}

	return email
}

// Creates a message containing the error to send to the original message author (and CC the admin).
func createErrorEmail(message *MailMessage, err error, numSent int, numTotal int) *ses.SendEmailInput {
	return &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("%s <%s>", "Mailing List Admin", "listadmin@team254.com")),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(message.from.Address)},
			CcAddresses: []*string{aws.String(adminAddress)},
		},
		Message: &ses.Message{
			Subject: &ses.Content{
				Data: aws.String(fmt.Sprintf("Failed to send message \"%s\"", message.subject)),
			},
			Body: &ses.Body{
				Html: &ses.Content{
					Data: aws.String(fmt.Sprintf("There was an error sending your message:<br /><br />%v"+
						"<br /><br />Message sent successfully to %d of %d recipients.", err, numSent, numTotal)),
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

	service := ses.New(awsSession)

	allRecipients, err := getRecipients(message.to)
	if err != nil {
		log.Printf("Error getting recipients: %v", err)
		email := createErrorEmail(message, err, 0, len(allRecipients))
		_, err := service.SendEmail(email)
		if err != nil {
			log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
		}
	}

	log.Printf("Redistributing message to %d recipients: %v", len(allRecipients), allRecipients)
	var actualRecipients []string
	if isDebug(message) {
		// Only send the message to the original sender.
		actualRecipients = []string{message.from.Address}
	} else {
		actualRecipients = allRecipients
	}
	for index, recipient := range actualRecipients {
		email := createEmail(message, recipient, allRecipients)
		_, err := service.SendEmail(email)
		if err != nil {
			log.Printf("Error sending message to %s: %v", recipient, err)
			email := createErrorEmail(message, err, index, len(actualRecipients))
			_, err := service.SendEmail(email)
			if err != nil {
				log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
			}
			break
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
