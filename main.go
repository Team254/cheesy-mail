package main

import (
	"log"
	"os"
)

func main() {
	logfile, err := os.OpenFile("cheesy-mail.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}
	log.SetOutput(logfile)

	configure()
	go runSmtpServer()
	log.Println("Listening for incoming mail.")

	for {
		message := <-messageReceivedChan
		log.Println("Message received:")
		log.Printf("From: %v", message.from)
		log.Printf("To: %v", message.to)
		log.Printf("Subject: %s", message.subject)
		log.Printf("Body: %s", message.body.HTML)
	}
}
