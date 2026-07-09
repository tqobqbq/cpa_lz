// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"fmt"
	"os"
	"strings"
)

// OAuth environment variable names and callback configuration.
const (
	ClientIDEnv     = "CPA_ANTIGRAVITY_OAUTH_CLIENT_ID"
	ClientSecretEnv = "CPA_ANTIGRAVITY_OAUTH_CLIENT_SECRET"
	CallbackPort    = 51121
)

// OAuthClientID returns the Antigravity OAuth client ID from environment.
func OAuthClientID() string {
	return strings.TrimSpace(os.Getenv(ClientIDEnv))
}

// OAuthClientCredentials returns Antigravity OAuth client credentials from environment.
func OAuthClientCredentials() (string, string, error) {
	clientID := OAuthClientID()
	clientSecret := strings.TrimSpace(os.Getenv(ClientSecretEnv))
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("antigravity oauth client credentials missing; set %s and %s", ClientIDEnv, ClientSecretEnv)
	}
	return clientID, clientSecret, nil
}

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
)

// Antigravity API configuration
const (
	APIEndpoint      = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion       = "v1internal"
)
