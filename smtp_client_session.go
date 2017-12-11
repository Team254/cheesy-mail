// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// SMTP client session implementation for receiving messages and passing them to a listener via channel.
// Inspired by https://github.com/flashmob/go-guerrilla.

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/jhillyerd/go.enmime"
	"io"
	"log"
	"net"
	"net/mail"
	"strings"
	"time"
)

// Progression of SMTP client session states.
const (
	OPEN_STATE = iota
	COMMAND_STATE
	DATA_STATE
)

type ClientSession struct {
	server     *SmtpServer
	state      int
	helo       string
	response   string
	data       string
	conn       net.Conn
	bufin      *bufio.Reader
	bufout     *bufio.Writer
	kill_time  int64
	errorCount int
}

// Handles the lifecycle of a client connection to the SMTP server.
func (client *ClientSession) HandleSession() {
	defer client.close()

	for {
		switch client.state {
		case OPEN_STATE:
			client.setResponse("220 %s SMTP cheesy-mail", client.server.hostName)
			client.state = COMMAND_STATE
		case COMMAND_STATE:
			input, err := client.read()
			if err != nil {
				log.Printf("Read error: %v", err)
				if err == io.EOF {
					// client closed the connection already
					return
				}
				if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
					// too slow, timeout
					return
				}
				break
			}
			input = strings.Trim(input, " \n\r")
			cmd := strings.ToUpper(input)
			switch {
			case strings.Index(cmd, "HELO") == 0:
				if len(input) > 5 {
					client.helo = input[5:]
				}
				client.setResponse("250 %s Hello", client.server.hostName)
			case strings.Index(cmd, "EHLO") == 0:
				if len(input) > 5 {
					client.helo = input[5:]
				}
				client.setResponse("250-%s Hello %s \r\n250-SIZE %d\r\n250 HELP", client.server.hostName,
					client.helo, maxMessageSizeBytes)
			case strings.Index(cmd, "MAIL FROM:") == 0:
				client.setResponse("250 Ok")
			case strings.Index(cmd, "XCLIENT") == 0:
				client.setResponse("250 OK")
			case strings.Index(cmd, "RCPT TO:") == 0:
				// Reject any messages outright not sent to the domain.
				if strings.Contains(input, client.server.hostName) {
					client.setResponse("250 Accepted")
				} else {
					log.Printf("Rejecting probable spam message sent to %s", input[8:])
					client.setResponse("450 Rejected")
					client.kill()
				}
			case strings.Index(cmd, "NOOP") == 0:
				client.setResponse("250 OK")
			case strings.Index(cmd, "RSET") == 0:
				client.setResponse("250 OK")
			case strings.Index(cmd, "DATA") == 0:
				client.setResponse("354 Enter message, ending with \".\" on a line by itself")
				client.state = DATA_STATE
			case strings.Index(cmd, "QUIT") == 0:
				client.setResponse("221 Bye")
				client.kill()
			default:
				client.setResponse("500 unrecognized command")
				client.errorCount++
				if client.errorCount > 3 {
					client.setResponse("500 Too many unrecognized commands")
					client.kill()
				}
			}
		case DATA_STATE:
			var err error
			client.data, err = client.read()
			if err == nil {
				message, err := client.parseMessage()
				if err == nil {
					client.server.messageReceivedChan <- message
					client.setResponse("250 OK: message queued for delivery")
				} else {
					log.Printf("Error parsing message: %v", err)
					client.setResponse("554 Error: message parsing failed")
				}
			} else {
				log.Printf("DATA read error: %v", err)
			}
			client.state = COMMAND_STATE
		}

		// Send the response back to the client.
		err := client.writeResponse()
		if err != nil {
			if err == io.EOF {
				// The client closed the connection.
				return
			}
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				// The connection exceeded the timeout.
				return
			}
		}
		if client.kill_time > 1 {
			return
		}
	}
}

func (client *ClientSession) setResponse(format string, a ...interface{}) {
	client.response = fmt.Sprintf(format, a...) + "\r\n"
}

func (client *ClientSession) close() {
	client.conn.Close()
}

func (client *ClientSession) kill() {
	client.kill_time = time.Now().Unix()
}

func (client *ClientSession) read() (input string, err error) {
	suffix := "\r\n"
	if client.state == DATA_STATE {
		suffix = "\r\n.\r\n"
	}

	for err == nil {
		client.conn.SetDeadline(time.Now().Add(timeoutSec * time.Second))
		reply, err := client.bufin.ReadString('\n')
		if reply != "" {
			input = input + reply
			if len(input) > maxMessageSizeBytes {
				err = fmt.Errorf("Maximum DATA size exceeded (%d)", maxMessageSizeBytes)
				return input, err
			}
		}
		if err != nil {
			break
		}
		if strings.HasSuffix(input, suffix) {
			break
		}
	}
	return input, err
}

func (client *ClientSession) writeResponse() error {
	client.conn.SetDeadline(time.Now().Add(timeoutSec * time.Second))
	size, err := client.bufout.WriteString(client.response)
	client.bufout.Flush()
	client.response = client.response[size:]
	return err
}

func (client *ClientSession) parseMessage() (*MailMessage, error) {
	parsedMessage, err := mail.ReadMessage(bytes.NewBufferString(client.data))
	if err != nil {
		return nil, err
	}

	var message MailMessage
	fromList, err := parsedMessage.Header.AddressList("From")
	if err != nil {
		return nil, err
	}
	message.from = fromList[0]
	message.to, err = parsedMessage.Header.AddressList("To")
	if err != nil {
		return nil, err
	}
	message.subject = parsedMessage.Header.Get("Subject")
	message.body, err = enmime.ParseMIMEBody(parsedMessage)
	if err != nil {
		return nil, err
	}

	// Handle feedback reports as enmime does not.
	if message.body.Root != nil && message.body.Root.ContentType() == "multipart/report" {
		for _, part := range message.body.Inlines {
			if part.ContentType() == "message/feedback-report" {
				message.body.Text += "\n" + string(part.Content())
			}
		}
	}

	return &message, nil
}
