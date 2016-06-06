/**
Copyright (c) 2012 Flashmob, GuerrillaMail.com

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the
Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE
WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR
OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/jhillyerd/go.enmime"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	state       int
	helo        string
	mail_from   string
	rcpt_to     string
	read_buffer string
	response    string
	address     string
	data        string
	time        int64
	conn        net.Conn
	bufin       *bufio.Reader
	bufout      *bufio.Writer
	kill_time   int64
	errors      int
	clientId    int64
}

type MailMessage struct {
	from    *mail.Address
	to      []*mail.Address
	subject string
	body    *enmime.MIMEBody
}

var max_size int // max email DATA size
var timeout time.Duration
var allowedHosts = make(map[string]bool, 15)
var sem chan int // currently active clients

var messageReceivedChan chan *MailMessage // Channel for sending received messages to for downstream processing.

// Configurable values.
var gConfig = map[string]string{
	"GSMTP_MAX_SIZE":       "15728640",
	"GSMTP_HOST_NAME":      "lists.team254.com", // This should also be set to reflect your RDNS
	"GSMTP_VERBOSE":        "N",
	"GSMTP_TIMEOUT":        "100", // how many seconds before timeout.
	"GM_ALLOWED_HOSTS":     "lists.team254.com",
	"GM_PRIMARY_MAIL_HOST": "lists.team254.com",
	"GM_MAX_CLIENTS":       "500",
}

func logln(level int, s string) {

	if gConfig["GSMTP_VERBOSE"] == "Y" {
		fmt.Println(s)
	}
	if level == 2 {
		log.Fatalf(s)
	}
	if len(gConfig["GSMTP_LOG_FILE"]) > 0 {
		log.Println(s)
	}
}

func configure() {
	// map the allow hosts for easy lookup
	if arr := strings.Split(gConfig["GM_ALLOWED_HOSTS"], ","); len(arr) > 0 {
		for i := 0; i < len(arr); i++ {
			allowedHosts[arr[i]] = true
		}
	}
	var n int
	var n_err error
	// sem is an active clients channel used for counting clients
	if n, n_err = strconv.Atoi(gConfig["GM_MAX_CLIENTS"]); n_err != nil {
		n = 50
	}
	// currently active client list
	sem = make(chan int, n)
	// database writing workers
	messageReceivedChan = make(chan *MailMessage, 10)
	// timeout for reads
	if n, n_err = strconv.Atoi(gConfig["GSMTP_TIMEOUT"]); n_err != nil {
		timeout = time.Duration(10)
	} else {
		timeout = time.Duration(n)
	}
	// max email size
	if max_size, n_err = strconv.Atoi(gConfig["GSMTP_MAX_SIZE"]); n_err != nil {
		max_size = 131072
	}

	return
}

func runSmtpServer() {
	go nginxHTTPAuth()

	// Start listening for SMTP connections
	listenAddress := fmt.Sprintf("0.0.0.0:%d", config.GetInt("smtp_port"))
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		logln(2, fmt.Sprintf("Cannot listen on port, %v", err))
	} else {
		logln(1, fmt.Sprintf("Listening on tcp %s", fmt.Sprintf("0.0.0.0:%d", config.GetInt("smtp_port"))))
	}
	var clientId int64
	clientId = 1
	for {
		conn, err := listener.Accept()
		if err != nil {
			logln(1, fmt.Sprintf("Accept error: %s", err))
			continue
		}
		logln(1, fmt.Sprintf(" There are now "+strconv.Itoa(runtime.NumGoroutine())+" serving goroutines"))
		sem <- 1 // Wait for active queue to drain.
		go handleClient(&Client{
			conn:     conn,
			address:  conn.RemoteAddr().String(),
			time:     time.Now().Unix(),
			bufin:    bufio.NewReader(conn),
			bufout:   bufio.NewWriter(conn),
			clientId: clientId,
		})
		clientId++
	}
}

func handleClient(client *Client) {
	defer closeClient(client)
	//	defer closeClient(client)
	greeting := "220 " + gConfig["GSMTP_HOST_NAME"] +
		" SMTP Guerrilla-SMTPd #" + strconv.FormatInt(client.clientId, 10) + " (" + strconv.Itoa(len(sem)) + ") " + time.Now().Format(time.RFC1123Z)
	for i := 0; i < 100; i++ {
		switch client.state {
		case 0:
			responseAdd(client, greeting)
			client.state = 1
		case 1:
			input, err := readSmtp(client)
			if err != nil {
				logln(1, fmt.Sprintf("Read error: %v", err))
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
				responseAdd(client, "250 "+gConfig["GSMTP_HOST_NAME"]+" Hello ")
			case strings.Index(cmd, "EHLO") == 0:
				if len(input) > 5 {
					client.helo = input[5:]
				}
				responseAdd(client, "250-"+gConfig["GSMTP_HOST_NAME"]+" Hello "+client.helo+"["+client.address+"]"+"\r\n"+"250-SIZE "+gConfig["GSMTP_MAX_SIZE"]+"\r\n"+"250 HELP")
			case strings.Index(cmd, "MAIL FROM:") == 0:
				if len(input) > 10 {
					client.mail_from = input[10:]
				}
				responseAdd(client, "250 Ok")
			case strings.Index(cmd, "XCLIENT") == 0:
				// Nginx sends this
				// XCLIENT ADDR=212.96.64.216 NAME=[UNAVAILABLE]
				client.address = input[13:]
				client.address = client.address[0:strings.Index(client.address, " ")]
				fmt.Println("client address:[" + client.address + "]")
				responseAdd(client, "250 OK")
			case strings.Index(cmd, "RCPT TO:") == 0:
				if len(input) > 8 {
					client.rcpt_to = input[8:]
				}
				responseAdd(client, "250 Accepted")
			case strings.Index(cmd, "NOOP") == 0:
				responseAdd(client, "250 OK")
			case strings.Index(cmd, "RSET") == 0:
				client.mail_from = ""
				client.rcpt_to = ""
				responseAdd(client, "250 OK")
			case strings.Index(cmd, "DATA") == 0:
				responseAdd(client, "354 Enter message, ending with \".\" on a line by itself")
				client.state = 2
			case strings.Index(cmd, "QUIT") == 0:
				responseAdd(client, "221 Bye")
				killClient(client)
			default:
				responseAdd(client, fmt.Sprintf("500 unrecognized command"))
				client.errors++
				if client.errors > 3 {
					responseAdd(client, fmt.Sprintf("500 Too many unrecognized commands"))
					killClient(client)
				}
			}
		case 2:
			var err error
			client.data, err = readSmtp(client)
			if err == nil {
				if processMail(client) {
					responseAdd(client, "250 OK: message queued for delivery")
				} else {
					responseAdd(client, "554 Error: transaction failed, blame it on the weather")
				}
			} else {
				logln(1, fmt.Sprintf("DATA read error: %v", err))
			}
			client.state = 1
		}
		// Send a response back to the client
		err := responseWrite(client)
		if err != nil {
			if err == io.EOF {
				// client closed the connection already
				return
			}
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				// too slow, timeout
				return
			}
		}
		if client.kill_time > 1 {
			return
		}
	}

}

func responseAdd(client *Client, line string) {
	client.response = line + "\r\n"
}
func closeClient(client *Client) {
	client.conn.Close()
	<-sem // Done; enable next client to run.
}
func killClient(client *Client) {
	client.kill_time = time.Now().Unix()
}

func readSmtp(client *Client) (input string, err error) {
	var reply string
	// Command state terminator by default
	suffix := "\r\n"
	if client.state == 2 {
		// DATA state
		suffix = "\r\n.\r\n"
	}
	for err == nil {
		client.conn.SetDeadline(time.Now().Add(timeout * time.Second))
		reply, err = client.bufin.ReadString('\n')
		if reply != "" {
			input = input + reply
			if len(input) > max_size {
				err = errors.New("Maximum DATA size exceeded (" + strconv.Itoa(max_size) + ")")
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

func responseWrite(client *Client) (err error) {
	var size int
	client.conn.SetDeadline(time.Now().Add(timeout * time.Second))
	size, err = client.bufout.WriteString(client.response)
	client.bufout.Flush()
	client.response = client.response[size:]
	return err
}

func processMail(client *Client) (success bool) {
	parsedMessage, err := mail.ReadMessage(bytes.NewBufferString(client.data))
	if err != nil {
		log.Printf("Error: %v\n", err)
		return false
	}
	var message MailMessage
	fromList, err := parsedMessage.Header.AddressList("From")
	if err != nil {
		log.Printf("Error: %v\n", err)
		return false
	}
	message.from = fromList[0]
	message.to, err = parsedMessage.Header.AddressList("To")
	if err != nil {
		log.Printf("Error: %v\n", err)
		return false
	}
	message.subject = parsedMessage.Header.Get("Subject")
	message.body, err = enmime.ParseMIMEBody(parsedMessage)
	if err != nil {
		log.Printf("Error: %v\n", err)
		return false
	}
	messageReceivedChan <- &message

	return true
}

func nginxHTTPAuth() {
	http.HandleFunc("/", nginxHTTPAuthHandler)
	err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", config.GetInt("http_port")), nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}

}

func nginxHTTPAuthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Auth-Status", "OK")
	w.Header().Add("Auth-Server", "0.0.0.0")
	w.Header().Add("Auth-Port", strconv.Itoa(config.GetInt("smtp_port")))
	fmt.Fprint(w, "")
}
