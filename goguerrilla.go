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
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/sloonz/go-iconv"
	"github.com/sloonz/go-qprintable"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"regexp"
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
	subject     string
	hash        string
	time        int64
	tls_on      bool
	conn        net.Conn
	bufin       *bufio.Reader
	bufout      *bufio.Writer
	kill_time   int64
	errors      int
	clientId    int64
}

type Mail struct {
	from    string
	to      string
	subject string
	body    string
}

var max_size int // max email DATA size
var timeout time.Duration
var allowedHosts = make(map[string]bool, 15)
var sem chan int // currently active clients

var mailReceivedChan chan *Mail // Channel for sending received messages to for downstream processing.

// Configurable values.
var gConfig = map[string]string{
	"GSMTP_MAX_SIZE":         "15728640",
	"GSMTP_HOST_NAME":        "lists.team254.com", // This should also be set to reflect your RDNS
	"GSMTP_VERBOSE":          "N",
	"GSMTP_TIMEOUT":          "100", // how many seconds before timeout.
	"GSTMP_LISTEN_INTERFACE": "0.0.0.0:8025",
	"GM_ALLOWED_HOSTS":       "lists.team254.com",
	"GM_PRIMARY_MAIL_HOST":   "lists.team254.com",
	"GM_MAX_CLIENTS":         "500",
	"NGINX_AUTH_ENABLED":     "Y",              // Y or N
	"NGINX_AUTH":             "127.0.0.1:8026", // If using Nginx proxy, ip and port to serve Auth requsts
	"SGID":                   "1008",           // group id
	"SUID":                   "1008",           // user id, from /etc/passwd
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
	mailReceivedChan = make(chan *Mail, 10)
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
	if gConfig["NGINX_AUTH_ENABLED"] == "Y" {
		go nginxHTTPAuth()
	}
	// Start listening for SMTP connections
	listener, err := net.Listen("tcp", gConfig["GSTMP_LISTEN_INTERFACE"])
	if err != nil {
		logln(2, fmt.Sprintf("Cannot listen on port, %v", err))
	} else {
		logln(1, fmt.Sprintf("Listening on tcp %s", gConfig["GSTMP_LISTEN_INTERFACE"]))
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
			if client.state == 2 {
				// Extract the subject while we are at it.
				scanSubject(client, reply)
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

// Scan the data part for a Subject line. Can be a multi-line
func scanSubject(client *Client, reply string) {
	if client.subject == "" && (len(reply) > 8) {
		test := strings.ToUpper(reply[0:9])
		if i := strings.Index(test, "SUBJECT: "); i == 0 {
			// first line with \r\n
			client.subject = reply[9:]
		}
	} else if strings.HasSuffix(client.subject, "\r\n") {
		// chop off the \r\n
		client.subject = client.subject[0 : len(client.subject)-2]
		if (strings.HasPrefix(reply, " ")) || (strings.HasPrefix(reply, "\t")) {
			// subject is multi-line
			client.subject = client.subject + reply[1:]
		}
	}
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
	user, _, addr_err := validateEmailData(client)
	if addr_err != nil { // user, host, addr_err
		fmt.Println(fmt.Sprintln("mail_from didnt validate: %v", addr_err) + " client.mail_from:" + client.mail_from)
		// notify client that a save completed, -1 = error
		return false
	}

	var mail Mail
	mail.from = client.mail_from
	mail.to = user + "@" + gConfig["GM_PRIMARY_MAIL_HOST"]
	mail.subject = mimeHeaderDecode(client.subject)
	mail.body = client.data
	mailReceivedChan <- &mail

	return true
}

func validateEmailData(client *Client) (user string, host string, addr_err error) {
	if user, host, addr_err = extractEmail(client.mail_from); addr_err != nil {
		return user, host, addr_err
	}
	client.mail_from = user + "@" + host
	if user, host, addr_err = extractEmail(client.rcpt_to); addr_err != nil {
		return user, host, addr_err
	}
	client.rcpt_to = user + "@" + host
	// check if on allowed hosts
	if allowed := allowedHosts[host]; !allowed {
		return user, host, errors.New("invalid host:" + host)
	}
	return user, host, addr_err
}

func extractEmail(str string) (name string, host string, err error) {
	re, _ := regexp.Compile(`<(.+?)@(.+?)>`) // go home regex, you're drunk!
	if matched := re.FindStringSubmatch(str); len(matched) > 2 {
		host = validHost(matched[2])
		name = matched[1]
	} else {
		if res := strings.Split(str, "@"); len(res) > 1 {
			name = res[0]
			host = validHost(res[1])
		}
	}
	if host == "" || name == "" {
		err = errors.New("Invalid address, [" + name + "@" + host + "] address:" + str)
	}
	return name, host, err
}

// Decode strings in Mime header format
// eg. =?ISO-2022-JP?B?GyRCIVo9dztSOWJAOCVBJWMbKEI=?=
func mimeHeaderDecode(str string) string {
	reg, _ := regexp.Compile(`=\?(.+?)\?([QBqp])\?(.+?)\?=`)
	matched := reg.FindAllStringSubmatch(str, -1)
	var charset, encoding, payload string
	if matched != nil {
		for i := 0; i < len(matched); i++ {
			if len(matched[i]) > 2 {
				charset = matched[i][1]
				encoding = strings.ToUpper(matched[i][2])
				payload = matched[i][3]
				switch encoding {
				case "B":
					str = strings.Replace(str, matched[i][0], mailTransportDecode(payload, "base64", charset), 1)
				case "Q":
					str = strings.Replace(str, matched[i][0], mailTransportDecode(payload, "quoted-printable", charset), 1)
				}
			}
		}
	}
	return str
}

func validHost(host string) string {
	host = strings.Trim(host, " ")
	re, _ := regexp.Compile(`^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]*[a-zA-Z0-9])\.)*([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9\-]*[A-Za-z0-9])$`)
	if re.MatchString(host) {
		return host
	}
	return ""
}

// decode from 7bit to 8bit UTF-8
// encoding_type can be "base64" or "quoted-printable"
func mailTransportDecode(str string, encoding_type string, charset string) string {
	if charset == "" {
		charset = "UTF-8"
	} else {
		charset = strings.ToUpper(charset)
	}
	if encoding_type == "base64" {
		str = fromBase64(str)
	} else if encoding_type == "quoted-printable" {
		str = fromQuotedP(str)
	}
	if charset != "UTF-8" {
		charset = fixCharset(charset)
		// eg. charset can be "ISO-2022-JP"
		convstr, err := iconv.Conv(str, "UTF-8", charset)
		if err == nil {
			return convstr
		}
	}
	return str
}

func fromBase64(data string) string {
	buf := bytes.NewBufferString(data)
	decoder := base64.NewDecoder(base64.StdEncoding, buf)
	res, _ := ioutil.ReadAll(decoder)
	return string(res)
}

func fromQuotedP(data string) string {
	buf := bytes.NewBufferString(data)
	decoder := qprintable.NewDecoder(qprintable.BinaryEncoding, buf)
	res, _ := ioutil.ReadAll(decoder)
	return string(res)
}

func fixCharset(charset string) string {
	reg, _ := regexp.Compile(`[_:.\/\\]`)
	fixed_charset := reg.ReplaceAllString(charset, "-")
	// Fix charset
	// borrowed from http://squirrelmail.svn.sourceforge.net/viewvc/squirrelmail/trunk/squirrelmail/include/languages.php?revision=13765&view=markup
	// OE ks_c_5601_1987 > cp949
	fixed_charset = strings.Replace(fixed_charset, "ks-c-5601-1987", "cp949", -1)
	// Moz x-euc-tw > euc-tw
	fixed_charset = strings.Replace(fixed_charset, "x-euc", "euc", -1)
	// Moz x-windows-949 > cp949
	fixed_charset = strings.Replace(fixed_charset, "x-windows_", "cp", -1)
	// windows-125x and cp125x charsets
	fixed_charset = strings.Replace(fixed_charset, "windows-", "cp", -1)
	// ibm > cp
	fixed_charset = strings.Replace(fixed_charset, "ibm", "cp", -1)
	// iso-8859-8-i -> iso-8859-8
	fixed_charset = strings.Replace(fixed_charset, "iso-8859-8-i", "iso-8859-8", -1)
	if charset != fixed_charset {
		return fixed_charset
	}
	return charset
}

func md5hex(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	sum := h.Sum([]byte{})
	return hex.EncodeToString(sum)
}

// If running Nginx as a proxy, give Nginx the IP address and port for the SMTP server
// Primary use of Nginx is to terminate TLS so that Go doesn't need to deal with it.
// This could perform auth and load balancing too
// See http://wiki.nginx.org/MailCoreModule
func nginxHTTPAuth() {
	parts := strings.Split(gConfig["GSTMP_LISTEN_INTERFACE"], ":")
	gConfig["HTTP_AUTH_HOST"] = parts[0]
	gConfig["HTTP_AUTH_PORT"] = parts[1]
	fmt.Println(parts)
	http.HandleFunc("/", nginxHTTPAuthHandler)
	err := http.ListenAndServe(gConfig["NGINX_AUTH"], nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}

}

func nginxHTTPAuthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Auth-Status", "OK")
	w.Header().Add("Auth-Server", gConfig["HTTP_AUTH_HOST"])
	w.Header().Add("Auth-Port", gConfig["HTTP_AUTH_PORT"])
	fmt.Fprint(w, "")
}
