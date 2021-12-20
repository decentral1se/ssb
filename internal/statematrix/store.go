// SPDX-FileCopyrightText: 2021 The Go-SSB Authors
//
// SPDX-License-Identifier: MIT

/*
Package statematrix stores and provides useful operations on an state matrix for the Epidemic Broadcast Tree protocol.

The state matrix represents multiple _network frontiers_ (or vector clock).

This version uses a SQL because that seems much handier to handle such an irregular sparse matrix.

Q:
* do we need a 2nd _told us about_ table?

*/
package statematrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.cryptoscope.co/ssb"
	refs "go.mindeco.de/ssb-refs"
	"go.mindeco.de/ssb-refs/tfk"
)

const onlyOwnerPerms = 0700

type StateMatrix struct {
	basePath string

	self string // whoami

	mu   sync.Mutex
	open currentFrontiers
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
		return ssb.NetworkFrontier{}, err
	}

	peerFile, err := os.Open(peerFileName)
	if err != nil {
		if !os.IsNotExist(err) {
			return ssb.NetworkFrontier{}, err
		}

		// new file, nothing to see here
		curr = ssb.NewNetworkFrontier()
		sm.open[peer.String()] = curr
		return curr, nil
	}
	defer peerFile.Close()

	curr = ssb.NewNetworkFrontier()
	err = json.NewDecoder(peerFile).Decode(&curr)
	if err != nil {
		return ssb.NetworkFrontier{}, fmt.Errorf("state json decode failed: %w", err)
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

	err = json.NewEncoder(peerFile).Encode(nf.Frontier)
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

		for feed, note := range selfNf.Frontier {

			theirNote, has := theirNf.Frontier[feed]
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

// WantsList returns all the feeds a peer wants to recevie messages for
func (sm *StateMatrix) WantsList(peer refs.FeedRef) ([]refs.FeedRef, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	nf, err := sm.loadFrontier(peer)
	if err != nil {
		return nil, err
	}

	var res []refs.FeedRef

	for feedStr, note := range nf.Frontier {
		if note.Receive {
			feed, err := refs.ParseFeedRef(feedStr)
			if err != nil {
				return nil, fmt.Errorf("wantList: failed to parse feed entry %q: %w", feedStr, err)
			}
			res = append(res, feed)
		}
	}

	return res, nil
}

// WantsFeed returns true if peer want's to receive feed
func (sm *StateMatrix) WantsFeed(peer, feed refs.FeedRef) (ssb.Note, bool, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	nf, err := sm.loadFrontier(peer)
	if err != nil {
		return ssb.Note{}, false, err
	}

	n, has := nf.Frontier[feed.String()]
	if !has {
		return ssb.Note{}, false, nil
	}

	return n, n.Receive, nil
}

// Changed returns which feeds have newer messages since last update
func (sm *StateMatrix) Changed(self, peer refs.FeedRef) (ssb.NetworkFrontier, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	selfNf, err := sm.loadFrontier(self)
	if err != nil {
		return ssb.NetworkFrontier{}, err
	}

	peerNf, err := sm.loadFrontier(peer)
	if err != nil {
		return ssb.NetworkFrontier{}, err
	}

	// calculate the subset of what self wants and peer wants to hear about
	relevant := ssb.NewNetworkFrontier()

	for wantedFeed, myNote := range selfNf.Frontier {
		theirNote, has := peerNf.Frontier[wantedFeed]
		if !has && myNote.Receive {
			// they don't have it, but tell them we want it
			relevant.Frontier[wantedFeed] = myNote
			continue
		}

		if !theirNote.Replicate {
			continue
		}

		if !theirNote.Receive && wantedFeed != peer.String() {
			// they dont care about this feed
			continue
		}

		relevant.Frontier[wantedFeed] = myNote
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
		return ssb.NetworkFrontier{}, err
	}

	// overwrite the entries in current with the updated ones
	for feed, note := range update.Frontier {
		current.Frontier[feed] = note
	}

	sm.open[who.String()] = current
	return current, nil
}

// Fill might be deprecated. It just updates the current frontier state
func (sm *StateMatrix) Fill(who refs.FeedRef, feeds []ObservedFeed) error {
	if len(feeds) == 0 { // noop
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	nf, err := sm.loadFrontier(who)
	if err != nil {
		return err
	}

	for _, updatedFeed := range feeds {
		if updatedFeed.Replicate {
			nf.Frontier[updatedFeed.Feed.String()] = updatedFeed.Note
		} else {
			// seq == -1 means drop it
			delete(nf.Frontier, updatedFeed.Feed.String())
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
