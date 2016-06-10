// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Mailing list e-mail server.

package main

import (
	"bytes"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
	"log"
	"os"
	"strings"
	"text/template"
	"time"
)

const (
	parentList  = "MAILINGLIST_PARENTS"
	studentList = "MAILINGLIST_STUDENTS"
)

var awsSession *session.Session
var config *Config
var listMap = map[string]string{"parents@lists.team254.com": parentList,
	"students@lists.team254.com": studentList}

// Returns the list of all addresses to distribute the message to, given the list addresses the original
// message was sent to.
func getRecipients(lists []string) ([]string, error) {
	recipientSet := make(map[string]struct{}) // Simulates a set for deduplication

	for _, list := range lists {
		users, err := GetUsersByPermission(list + "_RECEIVE")
		if err != nil {
			return nil, err
		}
		log.Printf("Recipients: %v", users) // TODO delete
		for _, user := range users {
			recipientSet[user.Email] = struct{}{}
		}
	}

	var recipients []string
	for recipient := range recipientSet {
		recipients = append(recipients, recipient)
	}
	return recipients, nil
}

func formatSubject(lists []string, subject string) string {
	prefix := "[Team 254"
	if len(lists) == 1 {
		if lists[0] == parentList {
			prefix += " Parents"
		} else if lists[0] == studentList {
			prefix += " Students"
		}
	}
	return prefix + "] " + subject
}

// Returns true if the subject line contains "DEBUG".
func isDebug(message *MailMessage) bool {
	return strings.Contains(message.subject, "DEBUG")
}

func createEmail(message *MailMessage, lists []string, recipient string, allRecipients []string) (*ses.SendEmailInput, error) {
	location, _ := time.LoadLocation("America/Los_Angeles")
	sendTime := time.Now().In(location)
	data := struct {
		Body          string
		IsDebug       bool
		AllRecipients []string
		Date          string
	}{message.body.HTML, isDebug(message), allRecipients, sendTime.Format("January 2, 2006")}
	template, err := template.ParseFiles("message.html")
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		return nil, err
	}

	return &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("%s <%s>", message.from.Name, config.GetString("from_address"))),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(recipient)},
		},
		ReplyToAddresses: []*string{aws.String(message.from.Address)},
		Message: &ses.Message{
			Subject: &ses.Content{
				Data: aws.String(formatSubject(lists, message.subject)),
			},
			Body: &ses.Body{
				Html: &ses.Content{
					Data: aws.String(buffer.String()),
				},
			},
		},
	}, nil
}

// Creates a message containing the error to send to the original message author (and CC the admin).
func createErrorEmail(message *MailMessage, err error, numSent int, numTotal int) (*ses.SendEmailInput, error) {
	data := struct {
		ErrorMessage string
		NumSent      int
		NumTotal     int
	}{err.Error(), numSent, numTotal}
	template, err := template.ParseFiles("error_message.html")
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		return nil, err
	}

	return &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("%s <%s>", "Mailing List Admin", "listadmin@team254.com")),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(message.from.Address)},
			CcAddresses: []*string{aws.String(config.GetString("admin_address"))},
		},
		Message: &ses.Message{
			Subject: &ses.Content{
				Data: aws.String(fmt.Sprintf("Failed to send message \"%s\"", message.subject)),
			},
			Body: &ses.Body{
				Html: &ses.Content{
					Data: aws.String(buffer.String()),
				},
			},
		},
	}, nil
}

func handleMessage(message *MailMessage) {
	log.Println("Message received:")
	log.Printf("From: %v", message.from)
	log.Printf("To: %v", message.to)
	log.Printf("Subject: %s", message.subject)
	log.Printf("Body: %s", message.body.HTML)
	log.Printf("Attachment count: %d", len(message.body.Attachments))
	log.Printf("Inline count: %d", len(message.body.Inlines))

	service := ses.New(awsSession)

	senderUser, err := GetUserByEmail(message.from.Address)
	if err != nil {
		log.Printf("Error looking up user: %v", err)
		return
	}

	// Determine which mailing lists the message is addressed to, and whether the sender has permission.
	var lists []string
	for _, toEmail := range message.to {
		if list, ok := listMap[toEmail.Address]; ok {
			hasPermission := false
			for _, permission := range senderUser.Permissions {
				if permission == list+"_SEND" {
					hasPermission = true
					break
				}
			}
			if !hasPermission {
				err = fmt.Errorf("Sender '%s' does not have permission to mail list '%s'.",
					message.from.Address, toEmail)
				log.Printf("Error: %v", err)
				email, err := createErrorEmail(message, err, 0, 0)
				if err == nil {
					_, err = service.SendEmail(email)
				}
				if err != nil {
					log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
				}
				return
			}
			lists = append(lists, list)
		}
	}

	if len(lists) == 0 {
		log.Printf("Message is not addressed to any known mailing lists; ignoring.")
		return
	}
	log.Printf("Lists addressed to: %v", lists)

	allRecipients, err := getRecipients(lists)
	if err != nil {
		log.Printf("Error getting recipients: %v", err)
		email, err := createErrorEmail(message, err, 0, len(allRecipients))
		if err == nil {
			_, err = service.SendEmail(email)
		}
		if err != nil {
			log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
		}
		return
	}

	// Reject any messages that contain attachments.
	if len(message.body.Attachments) > 0 || len(message.body.Inlines) > 0 {
		log.Println("Message contains attachments or inline images; rejecting.")
		err = fmt.Errorf("Attachments and inline images are not supported. Please use links instead.")
		email, err := createErrorEmail(message, err, 0, len(allRecipients))
		if err == nil {
			_, err = service.SendEmail(email)
		}
		if err != nil {
			log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
		}
		return
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
		email, err := createEmail(message, lists, recipient, allRecipients)
		if err == nil {
			_, err = service.SendEmail(email)
		}
		if err != nil {
			log.Printf("Error sending message to %s: %v", recipient, err)
			email, err := createErrorEmail(message, err, index, len(actualRecipients))
			if err == nil {
				_, err = service.SendEmail(email)
			}
			if err != nil {
				log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
			}
			break
		}

		// Sleep between sending messages to avoid exceeding the SES rate limit.
		time.Sleep(time.Millisecond * time.Duration(config.GetInt("send_interval_ms")))
	}
}

func main() {
	logfile, err := os.OpenFile("cheesy-mail.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}
	log.SetOutput(logfile)

	config, err = ReadConfig()
	if err != nil {
		log.Fatalf("Error reading configs: %v", err)
	}

	configure()
	go runSmtpServer()
	log.Println("Listening for incoming mail.")

	// Configure AWS client session.
	awsConfig := aws.Config{Region: aws.String(config.GetString("aws_region")),
		Credentials: credentials.NewStaticCredentials(config.GetString("aws_access_key_id"),
			config.GetString("aws_secret_access_key"), "")}
	awsSession = session.New(&awsConfig)

	for {
		message := <-messageReceivedChan
		handleMessage(message)
	}
}
