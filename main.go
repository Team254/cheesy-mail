// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Mailing list e-mail server.

package main

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
	"log"
	"os"
)

var config *Config
var sesService *ses.SES

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

	// Start the SMTP server.
	server := NewSmtpServer(config)
	go server.Run()

	// Configure AWS client session.
	awsConfig := aws.Config{Region: aws.String(config.GetString("aws_region")),
		Credentials: credentials.NewStaticCredentials(config.GetString("aws_access_key_id"),
			config.GetString("aws_secret_access_key"), "")}
	sesService = ses.New(session.New(&awsConfig))

	// Loop forever over incoming messages.
	for {
		message := <-server.messageReceivedChan
		message.Handle()
	}
}
