package model

import (
	"encoding/hex"
	"fmt"
	"strings"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Проверяет UUID и приводит его к строчному формату с дефисами.
func NormalizeID(raw string) (string, error) {
	value := raw
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", orderserrors.ErrInvalidInput
		}
		value = strings.ReplaceAll(value, "-", "")
	}
	if len(value) != 32 {
		return "", orderserrors.ErrInvalidInput
	}
	bytes, err := hex.DecodeString(value)
	if err != nil {
		return "", orderserrors.ErrInvalidInput
	}
	// Группы UUID содержат 8-4-4-4-12 шестнадцатеричных цифр.
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}
