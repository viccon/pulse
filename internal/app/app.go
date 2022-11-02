package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"code-harvest.conner.dev/internal/models"
	"code-harvest.conner.dev/internal/shared"
	"code-harvest.conner.dev/pkg/clock"
	"code-harvest.conner.dev/pkg/logger"
)

var HeartbeatTTL = time.Minute * 10
var heartbeatInterval = time.Second * 10

type storage interface {
	Connect() func()
	Save(s interface{}) error
}

type app struct {
	mutex          sync.Mutex
	clock          clock.Clock
	reader         MetadataReader
	storage        storage
	activeClientId string
	lastHeartbeat  int64
	session        *models.Session
	log            *logger.Logger
}

type option func(*app) error

func WithClock(clock clock.Clock) option {
	return func(a *app) error {
		if clock == nil {
			return errors.New("clock is nil")
		}
		a.clock = clock
		return nil
	}
}

func WithMetadataReader(reader MetadataReader) option {
	return func(a *app) error {
		if reader == nil {
			return errors.New("reader is nil")
		}
		a.reader = reader
		return nil
	}
}

func WithStorage(storage storage) option {
	return func(a *app) error {
		if storage == nil {
			return errors.New("storage is nil")
		}
		a.storage = storage
		return nil
	}
}

func WithLog(log *logger.Logger) option {
	return func(a *app) error {
		if log == nil {
			return errors.New("log is nil")
		}
		a.log = log
		return nil
	}
}

func New(opts ...option) (*app, error) {
	a := &app{
		clock:  clock.New(),
		reader: FileMetadataReader{},
	}
	for _, opt := range opts {
		err := opt(a)
		if err != nil {
			return &app{}, err
		}
	}
	return a, nil
}

// FocusGained should be called by the FocusGained autocommand. It gives us information
// about the currently active client. The duration of a coding session should not increase
// by the number of clients (VIM instances) we use. Only one will be tracked at a time.
func (app *app) FocusGained(event shared.Event, reply *string) error {
	// The heartbeat timer could fire at the exact same time.
	app.mutex.Lock()
	defer app.mutex.Unlock()

	app.lastHeartbeat = app.clock.GetTime()

	// When I jump between TMUX splits the *FocusGained* event in VIM will fire a
	// lot. I only want to end the current session, and create a new one, when I
	// open a new instance of VIM. If I'm, for example, jumping between a VIM split
	// and a terminal with test output I don't want it to result in a new coding session.
	if app.activeClientId == event.Id {
		app.log.PrintDebug("Jumped back to the same instance of VIM.", nil)
		return nil
	}

	// If the focus event is for the first instance of VIM we won't have any previous session.
	// That only occurs when using multiple splits with multiple instances of VIM.
	if app.session != nil {
		app.saveSession()
	}

	app.activeClientId = event.Id
	app.createSession(event.OS, event.Editor)

	// It could be an already existing VIM instance where a file buffer is already
	// open. If that is the case we can't count on getting the *OpenFile* event.
	// We might just be jumping between two VIM instances with one buffer each.
	app.updateCurrentFile(event.Path)

	*reply = "Successfully updated the client being focused."
	return nil
}

// OpenFile should be called by the *BufEnter* autocommand.
func (app *app) OpenFile(event shared.Event, reply *string) error {
	app.log.PrintDebug("Received OpenFile event", map[string]string{
		"path": event.Path,
	})

	// To not collide with the heartbeat check that runs on an interval.
	app.mutex.Lock()
	defer app.mutex.Unlock()

	app.lastHeartbeat = app.clock.GetTime()

	// The app won't receive any heartbeats if we open a buffer and then go AFK.
	// When that happens the session is ended. If we come back and either write the buffer,
	// or open a new file, we have to create a new session first.
	if app.session == nil {
		app.activeClientId = event.Id
		app.createSession(event.OS, event.Editor)
	}

	app.updateCurrentFile(event.Path)
	*reply = "Successfully updated the current file."
	return nil
}

// SendHeartbeat should be called when we want to inform the app that the session
// is still active. If we, for example, only edit a single file for a long time we
// can send it on a *BufWrite* autocommand.
func (app *app) SendHeartbeat(event shared.Event, reply *string) error {
	// In case the heartbeat check that runs on an interval occurs at the same time.
	app.mutex.Lock()
	defer app.mutex.Unlock()

	// This scenario would occur if we write the buffer when we have been
	// inactive for more than 10 minutes. The app will have ended our coding
	// session. Therefore, we have to create a new one.
	if app.session == nil {
		message := "The session was ended by a previous heartbeat check. Creating a new one."
		app.log.PrintDebug(message, map[string]string{
			"clientId": event.Id,
			"path":     event.Path,
		})
		app.activeClientId = event.Id
		app.createSession(event.OS, event.Editor)
		app.updateCurrentFile(event.Path)
	}

	// Update the time for the last heartbeat.
	app.lastHeartbeat = app.clock.GetTime()

	*reply = "Successfully sent heartbeat"
	return nil
}

// EndSession should be called by the *VimLeave* autocommand to inform the app that the session is done.
func (app *app) EndSession(event shared.Event, reply *string) error {
	app.mutex.Lock()
	defer app.mutex.Unlock()

	// We have reached an undesired state if we call end session and there is another
	// active client. It means that the events are sent in an incorrect order.
	if len(app.activeClientId) > 1 && app.activeClientId != event.Id {
		app.log.PrintFatal(errors.New("was called by a client that isn't considered active"), map[string]string{
			"actualClientId":   app.activeClientId,
			"expectedClientId": event.Id,
		})
	}

	// If we go AFK and don't send any heartbeats the session will have ended by
	// itself. If we then come back and exit VIM we will get the EndSession event
	// but won't have any session that we are tracking time for.
	if app.activeClientId == "" && app.session == nil {
		message := "The session was already ended, or possibly never started. Was there a previous hearbeat check?"
		app.log.PrintDebug(message, nil)
		return nil
	}

	app.saveSession()

	*reply = "The session was ended successfully."
	return nil
}

// Called by the ECG to determine whether the current session has gone stale or not.
func (app *app) CheckHeartbeat() {
	app.log.PrintDebug("Checking heartbeat", nil)
	if app.session != nil && app.lastHeartbeat+HeartbeatTTL.Milliseconds() < app.clock.GetTime() {
		app.mutex.Lock()
		defer app.mutex.Unlock()
		app.saveSession()
	}
}

func (app *app) archiveCurrentFile(closedAt int64) {
	if app.session.CurrentFile != nil {
		app.session.CurrentFile.ClosedAt = closedAt
		app.session.OpenFiles = append(app.session.OpenFiles, app.session.CurrentFile)
	}
}

func (app *app) updateCurrentFile(path string) {
	openedAt := app.clock.GetTime()

	fileMetadata, err := app.reader.Read(path)
	if err != nil {
		app.log.PrintDebug("Could not extract metadata for the path", map[string]string{
			"reason": err.Error(),
		})
		return
	}

	file := models.File{
		Name:       fileMetadata.Filename,
		Repository: fileMetadata.RepositoryName,
		Filetype:   fileMetadata.Filetype,
		Path:       path,
		OpenedAt:   openedAt,
		ClosedAt:   0,
	}

	// Update the current file.
	app.archiveCurrentFile(openedAt)
	app.session.CurrentFile = &file
	app.log.PrintDebug("Successfully updated the current file", map[string]string{
		"path": path,
	})
}

func (app *app) createSession(os, editor string) {
	app.session = &models.Session{
		StartedAt: time.Now().UTC().UnixMilli(),
		OS:        os,
		Editor:    editor,
		Files:     make(map[string]*models.File),
	}
}

func (app *app) saveSession() {
	// Regardless of how we exit this function we want to reset these values.
	defer func() {
		app.activeClientId = ""
		app.session = nil
	}()

	if app.session == nil {
		app.log.PrintDebug("There was no session to save.", nil)
		return
	}

	app.log.PrintDebug("Saving the session.", nil)

	// Set session duration and archive the current file.
	endedAt := app.clock.GetTime()
	app.archiveCurrentFile(endedAt)
	app.session.EndedAt = endedAt
	app.session.DurationMs = app.session.EndedAt - app.session.StartedAt

	// The OpenFiles list reflects all files we've opened. Each file has a
	// OpenedAt and ClosedAt property. Every file can appear more than once.
	// Before we save the session we aggregate this into a map where the key
	// is the name of the file and the value is a File with a merged duration
	// for all edits.
	if len(app.session.OpenFiles) > 0 {
		for _, f := range app.session.OpenFiles {
			currentFile, ok := app.session.Files[f.Path]
			if !ok {
				f.DurationMs = f.ClosedAt - f.OpenedAt
				app.session.Files[f.Path] = f
			} else {
				currentFile.DurationMs += f.ClosedAt - f.OpenedAt
			}
		}
	}

	if len(app.session.Files) < 1 {
		app.log.PrintDebug("The session had no files.", map[string]string{
			"clientId": app.activeClientId,
		})
		fmt.Println(app.session.Files)
		return
	}

	err := app.storage.Save(app.session)
	if err != nil {
		app.log.PrintError(err, nil)
	}
}

func startServer(app *app, port string) (net.Listener, error) {
	// The proxy exposes the functions that we want to make available for remote
	// procedure calls. Register the proxy as the RPC receiver.
	proxy := shared.NewServerProxy(app)
	err := rpc.RegisterName(shared.ServerName, proxy)
	if err != nil {
		return nil, err
	}

	rpc.HandleHTTP()
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return nil, err
	}

	err = http.Serve(listener, nil)
	return listener, err
}

func (app *app) Start(port string) error {
	app.log.PrintInfo("Starting up...", nil)

	// Connect to the storage
	disconnect := app.storage.Connect()
	defer disconnect()

	// Start the RPC server
	listener, err := startServer(app, port)
	if err != nil {
		app.log.PrintFatal(err, nil)
	}

	// Listen for shutdown channels and perform ECG checks.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	ecg := time.NewTicker(heartbeatInterval)

	run := true
	for run {
		select {
		case <-ecg.C:
			app.CheckHeartbeat()
		case <-quit:
			run = false
		}
	}

	app.log.PrintInfo("Shutting down...", nil)
	ecg.Stop()
	return listener.Close()
}
