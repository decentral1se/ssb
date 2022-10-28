// SPDX-FileCopyrightText: 2021 The Go-SSB Authors
//
// SPDX-License-Identifier: MIT

package statematrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pkg/errors"
	"github.com/ssbc/go-ssb/message"

	"github.com/ssbc/go-ssb"
	refs "github.com/ssbc/go-ssb-refs"
	"github.com/ssbc/go-ssb-refs/tfk"
)

const onlyOwnerPerms = 0700

type StateMatrix struct {
	basePath string

	self string // whoami

	mu   sync.Mutex
	open currentFrontiers

	wantList ssb.ReplicationLister
	verify   *message.VerificationRouter
}

// map[peer reference]frontier
type currentFrontiers map[string]ssb.NetworkFrontier

func New(base string, self refs.FeedRef) (*StateMatrix, error) {

	os.MkdirAll(base, onlyOwnerPerms)

	sm := StateMatrix{
		basePath: base,

		self: self.String(),

		open: make(currentFrontiers),
	}

	_, err := sm.loadFrontier(self)
	if err != nil {
		return nil, err
	}

	return &sm, nil
}

func (sm *StateMatrix) SetDependencies(wantList ssb.ReplicationLister, verify *message.VerificationRouter) {
	sm.wantList = wantList
	sm.verify = verify
}

// Inspect returns the current frontier for the passed peer
func (sm *StateMatrix) Inspect(peer refs.FeedRef) (ssb.NetworkFrontier, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.loadFrontier(peer)
}

func (sm *StateMatrix) StateFileName(peer refs.FeedRef) (string, error) {
	peerTfk, err := tfk.Encode(peer)
	if err != nil {
		return "", err
	}

	hexPeerTfk := fmt.Sprintf("%x", peerTfk)
	peerFileName := filepath.Join(sm.basePath, hexPeerTfk)
	return peerFileName, nil
}

func (sm *StateMatrix) loadFrontier(peer refs.FeedRef) (ssb.NetworkFrontier, error) {
	curr, has := sm.open[peer.String()]
	if has {
		return curr, nil
	}

	peerFileName, err := sm.StateFileName(peer)
	if err != nil {
		return nil, err
	}

	peerFile, err := os.Open(peerFileName)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		// new file, nothing to see here
		curr = make(ssb.NetworkFrontier)
		sm.open[peer.String()] = curr
		return curr, nil
	}
	defer peerFile.Close()

	curr = make(ssb.NetworkFrontier)
	err = json.NewDecoder(peerFile).Decode(&curr)
	if err != nil {
		return nil, err
	}
	sm.open[peer.String()] = curr

	return curr, nil
}

func (sm *StateMatrix) SaveAndClose(peer refs.FeedRef) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.saveAndClose(peer.String())
}

func (sm *StateMatrix) saveAndClose(peer string) error {
	parsed, err := refs.ParseFeedRef(peer)
	if err != nil {
		return err
	}

	err = sm.save(parsed)
	if err != nil {
		return err
	}

	delete(sm.open, peer)
	return nil
}

func (sm *StateMatrix) save(peer refs.FeedRef) error {
	peerFileName, err := sm.StateFileName(peer)
	if err != nil {
		return err
	}
	newPeerFileName := peerFileName + ".new"

	// truncate the file for overwriting, create it if it doesnt exist
	peerFile, err := os.OpenFile(newPeerFileName, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, onlyOwnerPerms)
	if err != nil {
		return err
	}

	nf, has := sm.open[peer.String()]
	if !has {
		return nil
	}

	err = json.NewEncoder(peerFile).Encode(nf)
	if err != nil {
		return err
	}

	// avoid weird behavior for renaming an open file.
	if err := peerFile.Close(); err != nil {
		return err
	}

	err = os.Rename(newPeerFileName, peerFileName)
	if err != nil {
		return fmt.Errorf("failed to replace %s with %s: %w", peerFileName, newPeerFileName, err)
	}

	return nil
}

type HasLongerResult struct {
	Peer refs.FeedRef
	Feed refs.FeedRef
	Len  uint64
}

func (hlr HasLongerResult) String() string {
	return fmt.Sprintf("%s: %s:%d", hlr.Peer.ShortSigil(), hlr.Feed.ShortSigil(), hlr.Len)
}

// HasLonger returns all the feeds which have more messages then we have and who has them.
func (sm *StateMatrix) HasLonger() ([]HasLongerResult, error) {
	var err error

	sm.mu.Lock()
	defer sm.mu.Unlock()

	selfNf, has := sm.open[sm.self]
	if !has {
		return nil, nil
	}

	var res []HasLongerResult

	for peer, theirNf := range sm.open {

		for feed, note := range selfNf {

			theirNote, has := theirNf[feed]
			if !has {
				continue
			}

			if theirNote.Seq > note.Seq {
				var hlr HasLongerResult
				hlr.Len = uint64(theirNote.Seq)

				hlr.Peer, err = refs.ParseFeedRef(peer)
				if err != nil {
					return nil, err
				}

				hlr.Feed, err = refs.ParseFeedRef(feed)
				if err != nil {
					return nil, err
				}

				res = append(res, hlr)
			}

		}
	}

	return res, nil
}

func (sm *StateMatrix) Changed(self, peer refs.FeedRef) (ssb.NetworkFrontier, error) {
	selfNf, err := sm.loadLocalFrontier()
	if err != nil {
		return nil, err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	relevant := make(ssb.NetworkFrontier)
	for wantedFeed, myNote := range selfNf {
		relevant[wantedFeed] = myNote
	}

	return relevant, nil
}

type ObservedFeed struct {
	Feed refs.FeedRef

	ssb.Note
}

// Update gets the current state from who, overwrites the notes in current with the new ones from the passed update
// and returns the complet updated frontier.
func (sm *StateMatrix) Update(who refs.FeedRef, update ssb.NetworkFrontier) (ssb.NetworkFrontier, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	current, err := sm.loadFrontier(who)
	if err != nil {
		return nil, err
	}

	// overwrite the entries in current with the updated ones
	for feed, note := range update {
		current[feed] = note
	}

	sm.open[who.String()] = current

	result := make(ssb.NetworkFrontier)
	for k, v := range current {
		result[k] = v
	}
	return result, nil
}

// Fill might be deprecated. It just updates the current frontier state
func (sm *StateMatrix) Fill(who refs.FeedRef, feeds []ObservedFeed) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	nf, err := sm.loadFrontier(who)
	if err != nil {
		return err
	}

	for _, updatedFeed := range feeds {
		if updatedFeed.Replicate {
			nf[updatedFeed.Feed.String()] = updatedFeed.Note
		} else {
			// seq == -1 means drop it
			delete(nf, updatedFeed.Feed.String())
		}
	}

	sm.open[who.String()] = nf
	return nil
}

func (sm *StateMatrix) Close() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for peer := range sm.open {
		sm.saveAndClose(peer)
	}

	return nil
}

func (sm *StateMatrix) loadLocalFrontier() (ssb.NetworkFrontier, error) {
	list, err := sm.wantList.ReplicationList().List()
	if err != nil {
		return ssb.NetworkFrontier{}, errors.Wrap(err, "replication list error")
	}

	frontier := make(ssb.NetworkFrontier)

	for _, ref := range list {
		sink, err := sm.verify.GetSink(ref)
		if err != nil {
			return ssb.NetworkFrontier{}, errors.Wrap(err, "failed to get verification sink")
		}

		frontier[ref.String()] = ssb.Note{
			Seq:       sink.Seq(),
			Replicate: true,
			Receive:   true,
		}
	}

	return frontier, nil
}
