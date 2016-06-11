// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Model and methods for distributing a message sent to a mailing list.

package main

import (
	"bytes"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ses"
	"github.com/jhillyerd/go.enmime"
	"log"
	"net/mail"
	"strings"
	"text/template"
	"time"
)

const (
	parentList  = "MAILINGLIST_PARENTS"
	studentList = "MAILINGLIST_STUDENTS"
)

var listMap = map[string]string{"parents@lists.team254.com": parentList,
	"students@lists.team254.com": studentList}

type MailMessage struct {
	from          *mail.Address
	to            []*mail.Address
	subject       string
	body          *enmime.MIMEBody
	lists         []string
	allRecipients []string
}

// Processes the incoming message and redistributes it to the appropriate recipients.
func (message *MailMessage) Handle() {
	log.Println("Message received:")
	log.Printf("From: %v", message.from)
	log.Printf("To: %v", message.to)
	log.Printf("Subject: %s", message.subject)
	log.Printf("Body: %s", message.body.HTML)
	log.Printf("Attachment count: %d", len(message.body.Attachments))
	log.Printf("Inline count: %d", len(message.body.Inlines))

	senderUser, err := GetUserByEmail(message.from.Address)
	if err != nil {
		log.Printf("Error looking up user: %v", err)
		return
	}

	message.lists, err = message.getListsAndCheckPermission(senderUser)
	if err != nil {
		message.handleError(err, 0)
		return
	}
	if len(message.lists) == 0 {
		log.Printf("Message is not addressed to any known mailing lists; ignoring.")
		return
	}
	log.Printf("Lists addressed to: %v", message.lists)

	message.allRecipients, err = message.getRecipients()
	if err != nil {
		message.handleError(err, 0)
		return
	}

	// Reject any messages that contain attachments.
	if len(message.body.Attachments) > 0 || len(message.body.Inlines) > 0 {
		log.Println("Message contains attachments or inline images; rejecting.")
		err = fmt.Errorf("Attachments and inline images are not supported. Please use links instead.")
		message.handleError(err, 0)
		return
	}

	log.Printf("Redistributing message to %d recipients: %v", len(message.allRecipients), message.allRecipients)
	var actualRecipients []string
	if message.isDebug() {
		// Only send the message to the original sender.
		actualRecipients = []string{message.from.Address}
	} else {
		actualRecipients = message.allRecipients
	}
	for index, recipient := range actualRecipients {
		err = message.forwardEmail(recipient)
		if err != nil {
			err = fmt.Errorf("Error sending message to %s: %v", recipient, err)
			message.handleError(err, index)
			return
		}

		// Sleep between sending messages to avoid exceeding the SES rate limit.
		time.Sleep(time.Millisecond * time.Duration(config.GetInt("send_interval_ms")))
	}
}

// Parses the mailing lists from the original recipients. Returns an error if the sender doesn't have permission.
func (message *MailMessage) getListsAndCheckPermission(senderUser *User) ([]string, error) {
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
				return nil, fmt.Errorf("Sender '%s' does not have permission to mail list '%s'.",
					message.from.Address, toEmail)
			}
			lists = append(lists, list)
		}
	}
	return lists, nil
}

// Returns the list of all addresses to distribute the message to, given the list addresses the original
// message was sent to.
func (message *MailMessage) getRecipients() ([]string, error) {
	recipientSet := make(map[string]struct{}) // Simulates a set for deduplication

	for _, list := range message.lists {
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

// Adds a list-specific prefix to the original subject.
func (message *MailMessage) getFormattedSubject() string {
	prefix := "[Team 254"
	if len(message.lists) == 1 {
		if message.lists[0] == parentList {
			prefix += " Parents"
		} else if message.lists[0] == studentList {
			prefix += " Students"
		}
	}
	return prefix + "] " + message.subject
}

// Returns true if the subject line contains "DEBUG".
func (message *MailMessage) isDebug() bool {
	return strings.Contains(message.subject, "DEBUG")
}

// Sends the reformatted original message on to the given recipient.
func (message *MailMessage) forwardEmail(recipient string) error {
	location, _ := time.LoadLocation("America/Los_Angeles")
	sendTime := time.Now().In(location)
	data := struct {
		Body          string
		IsDebug       bool
		AllRecipients []string
		Date          string
	}{message.body.HTML, message.isDebug(), message.allRecipients, sendTime.Format("January 2, 2006")}
	template, err := template.ParseFiles("message.html")
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		return err
	}

	email := &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("%s <%s>", message.from.Name, config.GetString("from_address"))),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(recipient)},
		},
		ReplyToAddresses: []*string{aws.String(message.from.Address)},
		Message: &ses.Message{
			Subject: &ses.Content{
				Data: aws.String(message.getFormattedSubject()),
			},
			Body: &ses.Body{
				Html: &ses.Content{
					Data: aws.String(buffer.String()),
				},
			},
		},
	}

	_, err = sesService.SendEmail(email)
	return err
}

// Sends a message containing the error to the original message author (and CCs the admin).
func (message *MailMessage) handleError(err error, numSent int) {
	log.Printf("Error: %v", err)

	data := struct {
		ErrorMessage string
		NumSent      int
		NumTotal     int
	}{err.Error(), numSent, len(message.allRecipients)}
	template, err := template.ParseFiles("error_message.html")
	if err != nil {
		log.Printf("Error: %v", err)
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		log.Printf("Error: %v", err)
	}

	email := &ses.SendEmailInput{
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
	}

	_, err = sesService.SendEmail(email)
	if err != nil {
		log.Printf("Error sending error notification to %s: %v", message.from.Address, err)
	}
}
