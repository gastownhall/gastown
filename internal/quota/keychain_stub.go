//go:build !darwin

package quota

import (
	"encoding/json"
	"errors"
)

var errNotDarwin = errors.New("keychain operations are only supported on macOS")

// KeychainCredential holds a backup of a keychain credential for rollback.
type KeychainCredential struct {
	ServiceName string
	Token       string
}

func KeychainServiceName(_ string) string                                          { return "" }
func ReadKeychainToken(_ string) (string, error)                                   { return "", errNotDarwin }
func WriteKeychainToken(_, _, _ string) error                                      { return errNotDarwin }
func SwapKeychainCredential(_, _ string) (*KeychainCredential, error)              { return nil, errNotDarwin }
func RestoreKeychainToken(_ *KeychainCredential) error                             { return errNotDarwin }
func SwapOAuthAccount(_, _ string) (json.RawMessage, error)                        { return nil, errNotDarwin }
func RestoreOAuthAccount(_ string, _ json.RawMessage) error                        { return errNotDarwin }
func ValidateKeychainToken(_ string) error                                         { return nil }
func ProbeAccountHTTP(_ string) (error, string)                                    { return errNotDarwin, "" }
func SyncSwappedTokens(_ map[string]string) int                                    { return 0 }
