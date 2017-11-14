// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Model and methods for distributing a message sent to a mailing list.

package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ses"
	"github.com/jhillyerd/go.enmime"
	"github.com/nu7hatch/gouuid"
	"io/ioutil"
	"log"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	parentList  = "MAILINGLIST_PARENTS"
	studentList = "MAILINGLIST_STUDENTS"
)

var listMap = map[string]string{"parents@lists.team254.com": parentList, "parents@team254.com": parentList,
	"students@lists.team254.com": studentList, "students@team254.com": studentList}
var base32Codec = base32.StdEncoding.WithPadding(base32.NoPadding)

type MailMessage struct {
	from          *mail.Address
	to            []*mail.Address
	subject       string
	body          *enmime.MIMEBody
	lists         []string
	allRecipients []string
	attachmentDir string
	attachments   []string
	inlines       []string
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

	if message.handleReplyForwarding() {
		return
	}

	senderUser, err := GetUserByEmail(message.from.Address)
	if err != nil {
		message.handleError(err, 0)
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

	if message.body.HTML == "" {
		err = errors.New("Rejected message with blank HTML body; can't process plain-text messages. " +
			"Please re-send as HTML (try using a different client).")
		message.handleError(err, 0)
		return
	}

	message.allRecipients, err = message.getRecipients()
	if err != nil {
		message.handleError(err, 0)
		return
	}

	err = message.saveAttachments()
	if err != nil {
		err = fmt.Errorf("Error saving attachments: %v", err)
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

	if !message.isDebug() {
		message.postToMattermost()

		err = message.postToBlog(senderUser)
		if err != nil {
			err = fmt.Errorf("Error posting message to blog after distributing to list: %v", err)
			message.handleError(err, len(actualRecipients))
			return
		}
	}
}

// Parses the mailing lists from the original recipients. Returns an error if the sender doesn't have permission.
func (message *MailMessage) getListsAndCheckPermission(senderUser *User) ([]string, error) {
	var lists []string
	for _, toEmail := range message.to {
		if list, ok := listMap[strings.ToLower(toEmail.Address)]; ok {
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
		for _, user := range users {
			recipientSet[user.Email] = struct{}{}
		}
	}

	var recipients sort.StringSlice
	for recipient := range recipientSet {
		recipients = append(recipients, recipient)
	}
	recipients.Sort()
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

// Saves attachments to a local directory that is served via HTTP.
func (message *MailMessage) saveAttachments() error {
	if len(message.body.Attachments) == 0 && len(message.body.Inlines) == 0 {
		return nil
	}

	messageId, _ := uuid.NewV4()
	message.attachmentDir = messageId.String()
	basePath := fmt.Sprintf("%s/%s", config.GetString("attachment_save_path"), message.attachmentDir)
	err := os.MkdirAll(basePath, 0755)
	if err != nil {
		return err
	}

	for _, attachment := range message.body.Attachments {
		filePath := fmt.Sprintf("%s/%s", basePath, attachment.FileName())
		err = ioutil.WriteFile(filePath, attachment.Content(), 0644)
		if err != nil {
			return err
		}
		message.attachments = append(message.attachments, attachment.FileName())
	}

	for _, inline := range message.body.Inlines {
		filePath := fmt.Sprintf("%s/%s", basePath, inline.FileName())
		err = ioutil.WriteFile(filePath, inline.Content(), 0644)
		if err != nil {
			return err
		}
		message.inlines = append(message.inlines, inline.FileName())

		// Rewrite the image tag in the HTML body to link to the inline image.
		cid := inline.Header().Get("X-Attachment-Id")
		inlineImageUrl := fmt.Sprintf("%s/%s/%s", config.GetString("attachment_base_url"),
			message.attachmentDir, inline.FileName())
		imageRe := regexp.MustCompile(fmt.Sprintf("<img src=[\"'](cid:%s)[\"']", cid))
		matches := imageRe.FindStringSubmatch(message.body.HTML)
		if matches == nil {
			return fmt.Errorf("Could not find content ID '%s' in message body.", cid)
		}
		message.body.HTML = strings.Replace(message.body.HTML, matches[1], inlineImageUrl, -1)
	}

	return nil
}

// Sends the reformatted original message on to the given recipient.
func (message *MailMessage) forwardEmail(recipient string) error {
	location, _ := time.LoadLocation("America/Los_Angeles")
	sendTime := time.Now().In(location)
	attachmentBaseUrl := fmt.Sprintf("%s/%s", config.GetString("attachment_base_url"), message.attachmentDir)
	data := struct {
		Body              string
		IsDebug           bool
		AllRecipients     []string
		Date              string
		AttachmentBaseUrl string
		Attachments       []string
	}{message.body.HTML, message.isDebug(), message.allRecipients, sendTime.Format("January 2, 2006"),
		attachmentBaseUrl, message.attachments}
	template, err := template.ParseFiles("message.html")
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		return err
	}

	encodedFromAddress := strings.ToLower(base32Codec.EncodeToString([]byte(message.from.Address)))
	email := &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("\"%s\" <r-%s@%s>", message.from.Name, encodedFromAddress,
			config.GetString("host_name"))),
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

func (message *MailMessage) postToBlog(senderUser *User) error {
	// Format the message as an HTML blog post.
	attachmentBaseUrl := fmt.Sprintf("%s/%s", config.GetString("attachment_base_url"), message.attachmentDir)
	data := struct {
		Body              string
		AttachmentBaseUrl string
		Attachments       []string
	}{message.body.HTML, attachmentBaseUrl, message.attachments}
	template, err := template.ParseFiles("blog_post.html")
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		return err
	}
	body := buffer.String()

	// Determine which lists the message was sent to, so that it can be posted in the appropriate category.
	sentToStudents := "0"
	sentToParents := "0"
	for _, list := range message.lists {
		if list == studentList {
			sentToStudents = "1"
		} else if list == parentList {
			sentToParents = "1"
		}
	}

	url := config.GetString("blog_post_url")
	client := &http.Client{}
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(body))
	if err != nil {
		return err
	}

	// Populate post metadata.
	location, _ := time.LoadLocation("America/Los_Angeles")
	sendTime := time.Now().In(location)
	dateString := sendTime.Format("Mon, 2 Jan 2006 15:04:05 MST")
	req.Header.Set("Date", dateString)
	req.Header.Set("User-Agent", "cheesy-mail")
	authDigest := md5.Sum([]byte(dateString + message.subject + body + os.Getenv("TEAM254_SECRET")))
	req.Header.Set("Authorization", hex.EncodeToString(authDigest[:]))
	req.Header.Set("Poof-Title", message.subject)
	req.Header.Set("Poof-User", strconv.Itoa(senderUser.Id))
	req.Header.Set("Poof-Students", sentToStudents)
	req.Header.Set("Poof-Parents", sentToParents)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)
		log.Printf("Error posting blog: %s", string(body))
		return fmt.Errorf("Post failed: status code %d for URL %s", resp.StatusCode, url)
	}
	return nil
}

// Sends email data to a Mattermost webhook to post on the town-square channel
func (message *MailMessage) postToMattermost() {
	body := fmt.Sprintf("@channel:\n ### %s\n _From %s_\n %s", message.subject, message.from.Name, message.body.Text)

	data := struct {
		Channel  string `json:"channel"`
		Username string `json:"username"`
		Icon_url string `json:"icon_url"`
		Text     string `json:"text"`
	}{config.GetString("mattermost_channel_name"), config.GetString("mattermost_bot_username"), config.GetString("mattermost_icon_url"), body}

    jsonData, err := json.Marshal(data)
    if err != nil {
		log.Printf("Error: %v", err)
		return
    }
	
	req, err := http.NewRequest("POST", config.GetString("mattermost_post_url"), strings.NewReader(string(jsonData)))
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}
	defer resp.Body.Close()
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
		Source: aws.String(fmt.Sprintf("%s <%s>", "Mailing List Admin", config.GetString("admin_address"))),
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

// Forwards replies sent to r:[encoded reply address]@[list domain] to the appropriate user. Returns true if such a
// message was detected, even if it couldn't be handled properly.
func (message *MailMessage) handleReplyForwarding() bool {
	replyRe := regexp.MustCompile(fmt.Sprintf("^r-([a-z2-7]+)@%s$", config.GetString("host_name")))
	var replyAddresses []string
	for _, address := range message.to {
		matches := replyRe.FindStringSubmatch(address.Address)
		if len(matches) > 0 {
			replyAddress, err := base32Codec.DecodeString(strings.ToUpper(matches[1]))
			if err != nil {
				fmt.Println("error:", err)
				err = fmt.Errorf("Error decoding reply address: %v", err)
				message.handleError(err, 0)
				return true
			}
			replyAddresses = append(replyAddresses, string(replyAddress))
		}
	}
	if len(replyAddresses) == 0 {
		// This message does not represent a reply to a previous message sent out on the list.
		return false
	}

	log.Printf("Decoded reply-to addresses: %v", replyAddresses)
	data := struct {
		From     *mail.Address
		HtmlBody string
		TextBody string
	}{message.from, message.body.HTML, message.body.Text}
	template, err := template.ParseFiles("reply.html")
	if err != nil {
		message.handleError(err, 0)
		return true
	}
	var buffer bytes.Buffer
	err = template.Execute(&buffer, data)
	if err != nil {
		message.handleError(err, 0)
		return true
	}
	encodedFromAddress := strings.ToLower(base32Codec.EncodeToString([]byte(message.from.Address)))
	email := &ses.SendEmailInput{
		Source: aws.String(fmt.Sprintf("\"%s\" <r-%s@%s>", message.from.Name, encodedFromAddress,
			config.GetString("host_name"))),
		Destination: &ses.Destination{
			ToAddresses: aws.StringSlice(replyAddresses),
			CcAddresses: []*string{aws.String(config.GetString("admin_address"))},
		},
		ReplyToAddresses: []*string{aws.String(message.from.Address)},
		Message: &ses.Message{
			Subject: &ses.Content{
				Data: aws.String(message.subject),
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
		err = fmt.Errorf("Error forwarding reply: %v", err)
		message.handleError(err, 0)
	}

	return true
}
