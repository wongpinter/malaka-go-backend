package id

import (
	"github.com/google/uuid"
)

func New() uuid.UUID {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return u
}

func Parse(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

func MustParse(s string) uuid.UUID {
	return uuid.MustParse(s)
}

func IsValid(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
