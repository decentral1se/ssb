// SPDX-FileCopyrightText: 2021 The Go-SSB Authors
//
// SPDX-License-Identifier: MIT

package private_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ssbc/go-luigi"
	"github.com/ssbc/margaret"
	librarian "github.com/ssbc/margaret/indexes"
	kitlog "go.mindeco.de/log"

	"github.com/ssbc/go-ssb"
	"github.com/ssbc/go-ssb/client"
	"github.com/ssbc/go-ssb/internal/storedrefs"
	"github.com/ssbc/go-ssb/multilogs"
	"github.com/ssbc/go-ssb/private"
	"github.com/ssbc/go-ssb/sbot"
	refs "github.com/ssbc/go-ssb-refs"
)

func TestPrivatePublish(t *testing.T) {
	t.Run("classic", testPublishPerAlgo(refs.RefAlgoFeedSSB1))
	t.Run("gabby", testPublishPerAlgo(refs.RefAlgoFeedGabby))
}

func testPublishPerAlgo(algo refs.RefAlgo) func(t *testing.T) {
	return func(t *testing.T) {
		r, a := require.New(t), assert.New(t)

		srvRepo := filepath.Join("testrun", t.Name(), "serv")
		os.RemoveAll(srvRepo)

		alice, err := ssb.NewKeyPair(bytes.NewReader(bytes.Repeat([]byte("alice"), 8)), algo)
		r.NoError(err)

		srvLog := kitlog.NewNopLogger()
		if testing.Verbose() {
			srvLog = kitlog.NewJSONLogger(os.Stderr)
		}

		srv, err := sbot.New(
			sbot.WithKeyPair(alice),
			sbot.WithInfo(srvLog),
			sbot.WithRepoPath(srvRepo),
			sbot.WithListenAddr(":0"),
			sbot.LateOption(sbot.WithUNIXSocket()),
		)
		r.NoError(err, "failed to init sbot")

		const n = 32
		for i := n; i > 0; i-- {
			_, err := srv.PublishLog.Publish(struct {
				Type string `json:"type"`
				Text string
				I    int
			}{"test", "clear text!", i})
			r.NoError(err)
		}

		r.NoError(err, "sbot srv init failed")

		c, err := client.NewUnix(filepath.Join(srvRepo, "socket"))
		r.NoError(err, "failed to make client connection")

		type msg struct {
			Type string `json:"type"`
			Msg  string
		}
		ref, err := c.PrivatePublish(msg{"test", "hello, world"}, alice.ID())
		r.NoError(err, "failed to publish")
		r.NotNil(ref)

		src, err := c.PrivateRead()
		r.NoError(err, "failed to open private stream")

		more := src.Next(context.TODO())
		r.True(more, "expected to get a message")

		var savedMsg refs.KeyValueRaw
		rawMsg, err := src.Bytes()
		r.NoError(err, "failed to get msg")
		err = json.Unmarshal(rawMsg, &savedMsg)
		r.NoError(err, "failed to unnpack msg")

		if !a.True(savedMsg.Key().Equal(ref)) {
			whoops, err := srv.Get(ref)
			r.NoError(err)
			t.Log(string(whoops.ContentBytes()))
		}

		more = src.Next(context.TODO())
		r.False(more)

		// try with seqwrapped query
		pl, ok := srv.GetMultiLog(multilogs.IndexNamePrivates)
		r.True(ok)

		userPrivs, err := pl.Get(librarian.Addr("box1:") + storedrefs.Feed(srv.KeyPair.ID()))
		r.NoError(err)

		unboxlog := private.NewUnboxerLog(srv.ReceiveLog, userPrivs, srv.KeyPair)

		lsrc, err := unboxlog.Query(margaret.SeqWrap(true))
		r.NoError(err)

		v, err := lsrc.Next(context.TODO())
		r.NoError(err, "failed to get msg")

		sw, ok := v.(margaret.SeqWrapper)
		r.True(ok, "wrong type: %T", v)
		wrappedVal := sw.Value()
		wrappedMsg, ok := wrappedVal.(refs.Message)
		r.True(ok, "wrong type: %T", wrappedVal)
		r.Equal(wrappedMsg.Key().String(), ref.String())

		v, err = lsrc.Next(context.TODO())
		r.Error(err)
		r.True(errors.Is(err, luigi.EOS{}))

		// shutdown
		a.NoError(c.Close())
		srv.Shutdown()
		r.NoError(srv.Close())
	}
}
