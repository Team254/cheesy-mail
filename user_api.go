package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

type User struct {
	Id          int      `json:"id"`
	Email       string   `json:"email"`
	Permissions []string `json:"permissions"`
}

// Returns the list of user objects having the given permission.
func GetUsersByPermission(permission string) ([]*User, error) {
	body, err := getApiRequest("/users?permission=" + permission)
	if err != nil {
		return nil, err
	}

	var users []*User
	err = json.Unmarshal([]byte(body), &users)
	if err != nil {
		return nil, err
	}

	return users, nil
}

// Returns the user object having the given e-mail address, or an error if there is not exactly one.
func GetUserByEmail(email string) (*User, error) {
	body, err := getApiRequest("/users?email=" + email)
	if err != nil {
		return nil, err
	}

	var users []*User
	err = json.Unmarshal([]byte(body), &users)
	if err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("Sender %s is not a registered team254.com user.", email)
	}
	if len(users) != 1 {
		return nil, fmt.Errorf("Expected 1 user for address %s; got %d", email, len(users))
	}

	return users[0], nil
}

func (user *User) HasPermission(permission string) bool {
	for _, userPermission := range user.Permissions {
		if userPermission == permission {
			return true
		}
	}
	return false
}

func (user *User) UnsubscribeLink() string {
	if !user.HasPermission("MAILINGLIST_UNSUBSCRIBE") {
		return ""
	}

	hash := sha256.New()
	hash.Write([]byte(user.Email + config.GetString("mail_secret")))
	signature := hex.EncodeToString(hash.Sum(nil))

	return fmt.Sprintf("https://members.team254.com/mail/unsubscribe?email=%s&signature=%s", user.Email, signature)
}

func getApiRequest(path string) (string, error) {
	url := config.GetString("members_api_url") + path
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Get failed: status code %d for URL %s", resp.StatusCode, url)
	}

	// Get the response and handle errors
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// The API response is AES-encrypted; decrypt it.
	return Decrypt(string(body))
}
