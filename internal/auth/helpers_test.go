package auth

import (
	"os"
	"strings"
)

func readFile(p string) (string, error) { b, err := os.ReadFile(p); return string(b), err }
func contains(s, sub string) bool       { return strings.Contains(s, sub) }
