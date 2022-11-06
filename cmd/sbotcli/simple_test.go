// SPDX-FileCopyrightText: 2021 The Go-SSB Authors
//
// SPDX-License-Identifier: MIT

package main_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	refs "github.com/ssbc/go-ssb-refs"
	"github.com/ssbc/go-ssb/internal/testutils"
	"github.com/ssbc/go-ssb/sbot"
)

func buildCLI(t *testing.T) string {
	cliPath := filepath.Join("testrun", t.Name(), "sbotcli-test")
	err := exec.Command("go", "build", "-race", "-o", cliPath).Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(cliPath)
	})
	return cliPath
}

// returns a func which accepts CLI arguments, returns the output of running them. output is mirroed to stderr for assertions.
func mkCommandRunner(t *testing.T, ctx context.Context, path string, sockPath string) func(...string) ([]byte, []byte) {
	var stdout, stderr bytes.Buffer

	return func(args ...string) ([]byte, []byte) {
		stdout.Reset()
		stderr.Reset()

		argsWithSockPath := append([]string{"--unixsock", sockPath}, args...)

		sbotcli := exec.CommandContext(ctx, path, argsWithSockPath...)
		sbotcli.Stdout = io.MultiWriter(os.Stderr, &stdout)
		sbotcli.Stderr = io.MultiWriter(os.Stderr, &stderr)

		err := sbotcli.Run()
		if err != nil {
			t.Error(err)
		}

		return stdout.Bytes(), stderr.Bytes()
	}
}

func TestWhoami(t *testing.T) {
	cliPath := buildCLI(t)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	r, a := require.New(t), assert.New(t)

	srvRepo := filepath.Join("testrun", t.Name(), "serv")
	os.RemoveAll(srvRepo)
	srvLog := testutils.NewRelativeTimeLogger(nil)

	srv, err := sbot.New(
		sbot.WithInfo(srvLog),
		sbot.WithRepoPath(srvRepo),
		sbot.WithContext(ctx),
		sbot.WithListenAddr(":0"),
		sbot.LateOption(sbot.WithUNIXSocket()),
	)
	r.NoError(err, "sbot srv init failed")

	var errc = make(chan error)
	go func() {
		errc <- srv.Network.Serve(ctx)
	}()

	sbotcli := mkCommandRunner(t, ctx, cliPath, filepath.Join(srvRepo, "socket"))

	out, _ := sbotcli("call", "whoami")

	has := bytes.Contains(out, []byte(srv.KeyPair.ID().String()))
	a.True(has, "ID not found in output")

	srv.Shutdown()
	err = srv.Close()
	r.NoError(err)
	r.NoError(<-errc)
}

func TestGetSubset(t *testing.T) {
	cliPath := buildCLI(t)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	r, a := require.New(t), assert.New(t)

	srvRepo := filepath.Join("testrun", t.Name(), "serv")
	os.RemoveAll(srvRepo)
	srvLog := testutils.NewRelativeTimeLogger(os.Stderr)

	srv, err := sbot.New(
		sbot.WithInfo(srvLog),
		sbot.WithRepoPath(srvRepo),
		sbot.WithContext(ctx),
		sbot.WithListenAddr(":0"),
		sbot.LateOption(sbot.WithUNIXSocket()),
	)
	r.NoError(err, "sbot srv init failed")

	var errc = make(chan error)
	go func() {
		errc <- srv.Network.Serve(ctx)
	}()

	a.EqualValues(-1, srv.ReceiveLog.Seq(), "log not empty")

	sbotcli := mkCommandRunner(t, ctx, cliPath, filepath.Join(srvRepo, "socket"))

	getPosts := []string{"subset", `{"op":"type", "string": "post"}`}
	getFollows := []string{"subset", `{"op":"type", "string": "contact"}`}
	getByAuthor := []string{"subset", fmt.Sprintf(`{"op":"author", "feed": "%s"}`, srv.KeyPair.ID())}
	createPost := []string{"publish", "post", "post one: the first"}
	// first we populate db slightly
	_, _ = sbotcli(createPost...)
	// then we query it
	out, _ := sbotcli(getPosts...)
	has := bytes.Contains(out, []byte(`"type": "post"`))
	a.True(has, "subset returned a post as result")
	a.EqualValues(0, srv.ReceiveLog.Seq(), "first message")

	feedAlice, err := refs.NewFeedRefFromBytes(bytes.Repeat([]byte{1}, 32), refs.RefAlgoFeedSSB1)
	r.NoError(err)

	// publish a contact message for a new feed
	_, _ = sbotcli("publish", "contact", "--following", feedAlice.String())
	out, _ = sbotcli(getFollows...)
	a.EqualValues(1, srv.ReceiveLog.Seq(), "2nd message")
	has = bytes.Contains(out, []byte(`"type": "contact"`))
	a.True(has, "subset returned follow message in log")
	_, _ = sbotcli(getByAuthor...)

	srv.Shutdown()
	err = srv.Close()
	r.NoError(err)
	r.NoError(<-errc)
}

func TestPublish(t *testing.T) {
	cliPath := buildCLI(t)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	r, a := require.New(t), assert.New(t)

	srvRepo := filepath.Join("testrun", t.Name(), "serv")
	os.RemoveAll(srvRepo)
	srvLog := testutils.NewRelativeTimeLogger(os.Stderr)

	srv, err := sbot.New(
		sbot.WithInfo(srvLog),
		sbot.WithRepoPath(srvRepo),
		sbot.WithContext(ctx),
		sbot.WithListenAddr(":0"),
		sbot.LateOption(sbot.WithUNIXSocket()),
	)
	r.NoError(err, "sbot srv init failed")

	var errc = make(chan error)
	go func() {
		errc <- srv.Network.Serve(ctx)
	}()

	a.EqualValues(-1, srv.ReceiveLog.Seq(), "log not empty")

	sbotcli := mkCommandRunner(t, ctx, cliPath, filepath.Join(srvRepo, "socket"))

	out, _ := sbotcli("publish", "post", "hell, world!")

	has := bytes.Contains(out, []byte(".sha256"))
	a.True(has, "has a message hash")

	a.EqualValues(0, srv.ReceiveLog.Seq(), "first message")

	theFeed, err := refs.NewFeedRefFromBytes(bytes.Repeat([]byte{1}, 32), refs.RefAlgoFeedSSB1)
	r.NoError(err)

	out, _ = sbotcli("publish", "contact", "--following", theFeed.String())

	has = bytes.Contains(out, []byte(".sha256"))
	a.True(has, "has a message hash")

	a.EqualValues(1, srv.ReceiveLog.Seq(), "2nd message")

	srv.Shutdown()
	err = srv.Close()
	r.NoError(err)
	r.NoError(<-errc)
}

func TestGetPublished(t *testing.T) {
	cliPath := buildCLI(t)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	r, a := require.New(t), assert.New(t)

	srvRepo := filepath.Join("testrun", t.Name(), "serv")
	os.RemoveAll(srvRepo)
	srvLog := testutils.NewRelativeTimeLogger(os.Stderr)

	srv, err := sbot.New(
		sbot.WithInfo(srvLog),
		sbot.WithRepoPath(srvRepo),
		sbot.WithContext(ctx),
		sbot.WithListenAddr(":0"),
		sbot.LateOption(sbot.WithUNIXSocket()),
	)
	r.NoError(err, "sbot srv init failed")

	var errc = make(chan error)
	go func() {
		errc <- srv.Network.Serve(ctx)
	}()

	a.EqualValues(-1, srv.ReceiveLog.Seq(), "log not empty")

	sbotcli := mkCommandRunner(t, ctx, cliPath, filepath.Join(srvRepo, "socket"))
	out, _ := sbotcli("publish", "post", t.Name())

	a.EqualValues(0, srv.ReceiveLog.Seq(), "first message")

	has := bytes.Contains(out, []byte(".sha256"))
	a.True(has, "has a message hash")

	actualRef := strings.TrimSuffix(string(out), "\n")

	testMsgRef, err := refs.ParseMessageRef(actualRef)
	r.NoError(err)

	out, _ = sbotcli("get", testMsgRef.String())

	var msg map[string]interface{}
	err = json.Unmarshal(out, &msg)
	r.NoError(err)

	srv.Shutdown()
	err = srv.Close()
	r.NoError(err)
	r.NoError(<-errc)
}

func TestInviteCreate(t *testing.T) {
	cliPath := buildCLI(t)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	r, a := require.New(t), assert.New(t)

	srvRepo := filepath.Join("testrun", t.Name(), "serv")
	os.RemoveAll(srvRepo)
	srvLog := testutils.NewRelativeTimeLogger(os.Stderr)

	srv, err := sbot.New(
		sbot.WithInfo(srvLog),
		sbot.WithRepoPath(srvRepo),
		sbot.WithContext(ctx),
		sbot.WithListenAddr(":0"),
		sbot.LateOption(sbot.WithUNIXSocket()),
	)
	r.NoError(err, "sbot srv init failed")

	var errc = make(chan error)
	go func() {
		errc <- srv.Network.Serve(ctx)
	}()

	sbotcli := mkCommandRunner(t, ctx, cliPath, filepath.Join(srvRepo, "socket"))

	out, _ := sbotcli("invite", "create")
	srvPubKey := base64.StdEncoding.EncodeToString(srv.KeyPair.ID().PubKey())
	has := bytes.Contains(out, []byte(srvPubKey))
	a.True(has, "should have the srv's public key in it")

	tokenOut, _ := sbotcli("invite", "create", "--uses", "2")
	has = bytes.Contains(tokenOut, []byte(srvPubKey))
	a.True(has, "should have the srv's public key in it")

	token := string(tokenOut)

	out, _ = sbotcli("invite", "accept", token, srv.KeyPair.ID().String())
	has = bytes.Contains(out, []byte("accepted"))
	a.True(has, "should have been accepted")

	feedAlice, err := refs.NewFeedRefFromBytes(bytes.Repeat([]byte{1}, 32), refs.RefAlgoFeedSSB1)
	r.NoError(err)

	out, _ = sbotcli("invite", "accept", token, feedAlice.String())
	has = bytes.Contains(out, []byte("accepted"))
	a.True(has, "should have been accepted")

	srv.Shutdown()
	err = srv.Close()
	r.NoError(err)
	r.NoError(<-errc)
}
