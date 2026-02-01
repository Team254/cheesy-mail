// Copyright 2026 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Tests for message deduplication cache.

package main

import (
	"github.com/jhillyerd/go.enmime"
	"net/mail"
	"strings"
	"testing"
)

func TestMessageCache_IsDuplicate(t *testing.T) {
	cache := NewMessageCache()

	// First occurrence should not be duplicate
	if cache.IsDuplicate("key1") {
		t.Error("First occurrence of key1 should not be duplicate")
	}

	// Second occurrence should be duplicate
	if !cache.IsDuplicate("key1") {
		t.Error("Second occurrence of key1 should be duplicate")
	}

	// Different key should not be duplicate
	if cache.IsDuplicate("key2") {
		t.Error("First occurrence of key2 should not be duplicate")
	}

	// Original key should still be duplicate
	if !cache.IsDuplicate("key1") {
		t.Error("Third occurrence of key1 should still be duplicate")
	}
}

func TestMailMessage_GetDeduplicationKey_WithMessageID(t *testing.T) {
	message := &MailMessage{
		from:      &mail.Address{Address: "test@example.com"},
		subject:   "Test Subject",
		body:      &enmime.MIMEBody{Html: "<p>Test body</p>"},
		messageId: "<abc123@example.com>",
	}

	key := message.GetDeduplicationKey()
	expected := "msgid:<abc123@example.com>"
	if key != expected {
		t.Errorf("Expected key %q, got %q", expected, key)
	}
}

func TestMailMessage_GetDeduplicationKey_WithoutMessageID(t *testing.T) {
	message := &MailMessage{
		from:    &mail.Address{Address: "test@example.com"},
		subject: "Test Subject",
		body:    &enmime.MIMEBody{Html: "<p>Test body</p>"},
	}

	key := message.GetDeduplicationKey()
	if len(key) <= 5 {
		t.Error("Key should be longer than 5 characters")
	}
	if !strings.HasPrefix(key, "hash:") {
		t.Errorf("Key should start with 'hash:', got %q", key)
	}
}

func TestMailMessage_GetDeduplicationKey_SameContentSameHash(t *testing.T) {
	message1 := &MailMessage{
		from:    &mail.Address{Address: "test@example.com"},
		subject: "Test Subject",
		body:    &enmime.MIMEBody{Html: "<p>Test body</p>"},
	}

	message2 := &MailMessage{
		from:    &mail.Address{Address: "test@example.com"},
		subject: "Test Subject",
		body:    &enmime.MIMEBody{Html: "<p>Test body</p>"},
	}

	if message1.GetDeduplicationKey() != message2.GetDeduplicationKey() {
		t.Error("Messages with same content should have same deduplication key")
	}
}

func TestMailMessage_GetDeduplicationKey_DifferentContentDifferentHash(t *testing.T) {
	message1 := &MailMessage{
		from:    &mail.Address{Address: "test@example.com"},
		subject: "Test Subject",
		body:    &enmime.MIMEBody{Html: "<p>Test body</p>"},
	}

	message2 := &MailMessage{
		from:    &mail.Address{Address: "test@example.com"},
		subject: "Different Subject",
		body:    &enmime.MIMEBody{Html: "<p>Test body</p>"},
	}

	if message1.GetDeduplicationKey() == message2.GetDeduplicationKey() {
		t.Error("Messages with different content should have different deduplication keys")
	}
}
