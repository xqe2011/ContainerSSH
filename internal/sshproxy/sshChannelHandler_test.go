package sshproxy

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"go.containerssh.io/containerssh/log"
	"golang.org/x/crypto/ssh"
)

// fakeBackingChannel is a fake ssh.Channel simulating a backend program that only produces output after the
// client stdin has reached EOF (e.g. ssh -T host "echo PIPE_OK").
type fakeBackingChannel struct {
	lock             *sync.Mutex
	closeWriteCalled bool
	closeCalled      bool
	stdinEOF         chan struct{}
	output           []byte
	outputSent       bool
}

func newFakeBackingChannel(output []byte) *fakeBackingChannel {
	return &fakeBackingChannel{
		lock:     &sync.Mutex{},
		stdinEOF: make(chan struct{}),
		output:   output,
	}
}

func (f *fakeBackingChannel) Read(data []byte) (int, error) {
	// Hold back the program output until the proxy signals stdin EOF via CloseWrite.
	<-f.stdinEOF
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.outputSent {
		return 0, io.EOF
	}
	f.outputSent = true
	return copy(data, f.output), nil
}

func (f *fakeBackingChannel) Write(data []byte) (int, error) {
	return len(data), nil
}

func (f *fakeBackingChannel) Close() error {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.closeCalled = true
	return nil
}

func (f *fakeBackingChannel) CloseWrite() error {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.closeWriteCalled = true
	close(f.stdinEOF)
	return nil
}

func (f *fakeBackingChannel) SendRequest(_ string, _ bool, _ []byte) (bool, error) {
	return true, nil
}

func (f *fakeBackingChannel) Stderr() io.ReadWriter {
	return bytes.NewBuffer([]byte{})
}

// fakeSessionChannel is a fake sshserver.SessionChannel with an immediately-EOF stdin.
type fakeSessionChannel struct {
	stdout           *bytes.Buffer
	stderr           *bytes.Buffer
	closeWriteSignal chan struct{}
}

func newFakeSessionChannel() *fakeSessionChannel {
	return &fakeSessionChannel{
		stdout:           &bytes.Buffer{},
		stderr:           &bytes.Buffer{},
		closeWriteSignal: make(chan struct{}),
	}
}

func (f *fakeSessionChannel) Stdin() io.Reader {
	return bytes.NewReader([]byte{})
}

func (f *fakeSessionChannel) Stdout() io.Writer {
	return f.stdout
}

func (f *fakeSessionChannel) Stderr() io.Writer {
	return f.stderr
}

func (f *fakeSessionChannel) ExitStatus(_ uint32) {}

func (f *fakeSessionChannel) ExitSignal(_ string, _ bool, _ string, _ string) {}

func (f *fakeSessionChannel) CloseWrite() error {
	close(f.closeWriteSignal)
	return nil
}

func (f *fakeSessionChannel) Close() error {
	return nil
}

// TestStreamStdinEOFDoesNotCloseBackingChannel is a regression test for the bug where an EOF on the client
// stdin caused the backing channel to be closed entirely, killing the backend program with SIGPIPE before its
// output was delivered. The backing channel must only be closed for writing (EOF) so the backend output can
// still be streamed back to the client.
func TestStreamStdinEOFDoesNotCloseBackingChannel(t *testing.T) {
	backingChannel := newFakeBackingChannel([]byte("PIPE_OK\n"))
	session := newFakeSessionChannel()
	handler := &sshChannelHandler{
		backingChannel: backingChannel,
		session:        session,
		logger:         log.NewTestLogger(t),
		done:           make(chan struct{}),
	}

	if err := handler.streamStdio(); err != nil {
		t.Fatalf("failed to start stdio streaming (%v)", err)
	}

	select {
	case <-session.closeWriteSignal:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for the output streaming to complete")
	}

	backingChannel.lock.Lock()
	defer backingChannel.lock.Unlock()
	if !backingChannel.closeWriteCalled {
		t.Fatalf("backing channel was not closed for writing after stdin EOF")
	}
	if backingChannel.closeCalled {
		t.Fatalf("backing channel was closed on stdin EOF, must remain open for the backend output")
	}
	if stdout := session.stdout.String(); stdout != "PIPE_OK\n" {
		t.Fatalf("incorrect stdout received from backend: %q", stdout)
	}
}

var _ ssh.Channel = &fakeBackingChannel{}
