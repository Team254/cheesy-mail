// Copyright 2016 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Methods for reading environmment-specific configs from a JSON file.

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"strings"
)

type Config struct {
	params map[string]map[string]interface{}
}

// Parses the configs from JSON and returns an object the values can be accessed from.
func ReadConfig() (*Config, error) {
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(data, &config.params)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// Returns the value for the given key, cast as an integer.
func (config *Config) GetInt(key string) int {
	return int(config.getRawParam(key).(float64))
}

// Returns the value for the given key, cast as a string.
func (config *Config) GetString(key string) string {
	value := config.getRawParam(key).(string)
	if strings.HasPrefix(value, "Encrypted:") {
		decrypted, err := Decrypt(strings.TrimPrefix(value, "Encrypted:"))
		if err != nil {
			log.Fatalf("Error decrypting value for config key '%s': %v", key, err)
		}
		return decrypted
	} else {
		return value
	}
}

// Returns the environment-specific param for the given key, or logs a fatal error if it doesn't exist.
func (config *Config) getRawParam(key string) interface{} {
	environment := os.Getenv("TEAM254_ENV")
	if environment == "" {
		environment = "dev"
	}

	// Look in the environment-specific configs first.
	if _, ok := config.params[environment]; ok {
		value := config.params[environment][key]
		if value != nil {
			return value
		}
	}

	// Look in the global configs.
	if _, ok := config.params["global"]; ok {
		value := config.params["global"][key]
		if value != nil {
			return value
		}
	}

	log.Fatalf("Error: No value found for config key '%s'.", key)
	return nil
}

// Returns the plaintext for the given string encrypted with the Team 254 secret.
func Decrypt(encrypted string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}
	secret := os.Getenv("TEAM254_SECRET")
	if secret == "" {
		return "", errors.New("TEAM254_SECRET environment variable not set.")
	}
	secretDigest := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(secretDigest[:])
	if err != nil {
		return "", err
	}
	iv := make([]byte, aes.BlockSize)
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(data, data)
	// Remove any PKCS#7 padding.
	paddingSize := int(data[len(data)-1])
	return string(data[:len(data)-paddingSize]), nil
}
