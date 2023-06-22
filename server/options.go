package server

import (
	"errors"

	"code-harvest.conner.dev/clock"
	"code-harvest.conner.dev/domain"
	"code-harvest.conner.dev/filereader"
	"code-harvest.conner.dev/storage"
)

type option func(*server) error

// Clock is a simple abstraction that is used to simplify time based assertions in tests
type Clock interface {
	GetTime() int64
}

func WithClock(clock Clock) option {
	return func(a *server) error {
		if clock == nil {
			return errors.New("clock is nil")
		}
		a.clock = clock
		return nil
	}
}

type FileReader interface {
	GitFile(path string) (domain.GitFile, error)
}

func WithFileReader(reader FileReader) option {
	return func(a *server) error {
		if reader == nil {
			return errors.New("reader is nil")
		}
		a.fileReader = reader
		return nil
	}
}

func WithStorage(storage storage.TemporaryStorage) option {
	return func(a *server) error {
		if storage == nil {
			return errors.New("storage is nil")
		}
		a.storage = storage
		return nil
	}
}

type Log interface {
	PrintDebug(message string, properties map[string]string)
	PrintInfo(message string, properties map[string]string)
	PrintError(err error, properties map[string]string)
	PrintFatal(err error, properties map[string]string)
}

func WithLog(log Log) option {
	return func(a *server) error {
		if log == nil {
			return errors.New("log is nil")
		}
		a.log = log
		return nil
	}
}

func New(serverName string, opts ...option) (*server, error) {
	a := &server{
		serverName: serverName,
		clock:      clock.New(),
		fileReader: filereader.New(),
	}
	for _, opt := range opts {
		err := opt(a)
		if err != nil {
			return &server{}, err
		}
	}
	return a, nil
}