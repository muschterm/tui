//go:build !unix

package server

import (
	"errors"
	"time"
)

var errDocUnsupported = errors.New("shared documents are unsupported on this platform")

// docsSupported reports whether shared documents work on this platform.
const docsSupported = false

func observeDocFile(string, string) docObservation {
	return docObservation{Kind: docDiskUnavailable, Err: errDocUnsupported}
}

func statDocFile(string, string) (string, string) { return docDiskUnavailable, "" }

type docWrite struct {
	Token   string
	Obs     docObservation
	Renamed bool
}

func writeDocFile(string, string, []byte, uint32, []string, func(func() error) error) (docWrite, error) {
	return docWrite{Obs: docObservation{Kind: docDiskUnavailable, Err: errDocUnsupported}}, errDocUnsupported
}

func cleanupDocTemps(string, string, time.Time) {}
