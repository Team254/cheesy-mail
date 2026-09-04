// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Mailing list e-mail server.

package main

import (
	"context"
	"log"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ses"
)

var config *Config
var sesService interface {
	SendEmail(context.Context, *ses.SendEmailInput, ...func(*ses.Options)) (*ses.SendEmailOutput, error)
}
var messageCache *MessageCache

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

	messageCache = NewMessageCache()

	// Start the SMTP server.
	server := NewSmtpServer(config)
	go server.Run()

	// Configure AWS client session.
	awsConfig, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(config.GetString("aws_region")),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.GetString("aws_access_key_id"),
			config.GetString("aws_secret_access_key"),
			"",
		)),
	)
	if err != nil {
		log.Fatalf("Error configuring AWS: %v", err)
	}
	sesService = ses.NewFromConfig(awsConfig)

	// Loop forever over incoming messages.
	for {
		message := <-server.messageReceivedChan

		dedupKey := message.GetDeduplicationKey()
		if messageCache.IsDuplicate(dedupKey) {
			log.Printf("Duplicate message detected, skipping: From=%v, Subject=%s", message.from, message.subject)
			continue
		}

		message.Handle()
	}
}
