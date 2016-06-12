// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// SMTP server implementation for receiving messages and passing them to a listener via channel.
// Inspired by https://github.com/flashmob/go-guerrilla.

package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
)

const (
	maxMessageSizeBytes = 15728640
	timeoutSec          = 60
)

type SmtpServer struct {
	hostName            string
	smtpPort            int
	httpPort            int
	messageReceivedChan chan *MailMessage
}

func NewSmtpServer(config *Config) *SmtpServer {
	server := new(SmtpServer)
	server.hostName = config.GetString("host_name")
	server.smtpPort = config.GetInt("smtp_port")
	server.httpPort = config.GetInt("http_port")
	server.messageReceivedChan = make(chan *MailMessage, 10)
	return server
}

// Starts the SMTP server and loops indefinitely.
func (server *SmtpServer) Run() {
	// Start the local HTTP server that NGINX uses for auth.
	go server.runNginxHttp()

	// Start listening for SMTP connections.
	listenAddress := fmt.Sprintf("0.0.0.0:%d", server.smtpPort)
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		log.Fatalf("Cannot listen on port: %v", err)
	} else {
		log.Printf("SMTP server listening on %s", listenAddress)
	}

	// Loop forever over incoming connections.
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("SMTP accept error: %v", err)
			continue
		}

		// Handle each incoming request in a new goroutine.
		client := ClientSession{
			server: server,
			state:  OPEN_STATE,
			conn:   conn,
			bufin:  bufio.NewReader(conn),
			bufout: bufio.NewWriter(conn),
		}
		go client.HandleSession()
	}
}

func (server *SmtpServer) runNginxHttp() {
	http.Handle("/", server)
	err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", server.httpPort), nil)
	if err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}

// Responds to NGINX's auth requests with a set of headers pointing to the SMTP server to proxy to.
func (server *SmtpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Auth-Status", "OK")
	w.Header().Add("Auth-Server", "0.0.0.0")
	w.Header().Add("Auth-Port", strconv.Itoa(server.smtpPort))
}
