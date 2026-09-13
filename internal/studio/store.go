package studio

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("document not found")

type Document = map[string]any

type Record struct {
	ID   string
	Path string
	Data Document
}

type Query struct {
	Collection string
	Group      bool
	Order      string
	Desc       bool
	Limit      int
	After      string
}

type Transaction interface {
	Get(string) (Document, error)
	Set(string, Document, bool) error
	Delete(string) error
}

type Store interface {
	Get(context.Context, string) (Document, error)
	List(context.Context, Query) ([]Record, error)
	Set(context.Context, string, Document, bool) error
	Delete(context.Context, string) error
	Transact(context.Context, func(Transaction) error) error
}

type Identity interface {
	Login(context.Context, string, string) (string, string, error)
	Verify(context.Context, string) (string, error)
	CreateUser(context.Context, string, string, string) (string, error)
	DeleteUser(context.Context, string) error
}

type RateLimiter interface {
	Take(context.Context, string, int, time.Duration) error
}

type APIError struct {
	Status  int
	Message string
}

func (problem *APIError) Error() string { return problem.Message }

func fail(status int, message string) error {
	return &APIError{Status: status, Message: message}
}
