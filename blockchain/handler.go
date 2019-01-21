// Copyright 2018 The Fractal Team Authors
// This file is part of the fractal project.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package blockchain

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/fractalplatform/fractal/common"
	router "github.com/fractalplatform/fractal/event"
	"github.com/fractalplatform/fractal/types"
)

type BlockchainStation struct {
	station    router.Station
	peerCh     chan *router.Event
	blockchain *BlockChain
	networkId  uint64
	downloader *Downloader
}

func errResp(code errCode, format string, v ...interface{}) error {
	return fmt.Errorf("%v - %v", code, fmt.Sprintf(format, v...))
}

func newBlcokchainStation(bc *BlockChain, networkId uint64) *BlockchainStation {
	bs := &BlockchainStation{
		peerCh:     make(chan *router.Event),
		blockchain: bc,
		networkId:  networkId,
		downloader: NewDownloader(bc),
	}
	router.Subscribe(nil, bs.peerCh, router.P2pNewPeer, nil)
	router.Subscribe(nil, bs.peerCh, router.P2pDelPeer, nil)
	router.Subscribe(nil, bs.peerCh, router.DownloaderGetStatus, "")
	router.Subscribe(nil, bs.peerCh, router.DownloaderGetBlockHashMsg, &getBlcokHashByNumber{})
	router.Subscribe(nil, bs.peerCh, router.DownloaderGetBlockHeadersMsg, &getBlockHeadersData{})
	router.Subscribe(nil, bs.peerCh, router.DownloaderGetBlockBodiesMsg, []common.Hash{})

	go bs.loop()
	return bs
}

func (bs *BlockchainStation) chainStatus() *statusData {
	genesis := bs.blockchain.Genesis()
	head := bs.blockchain.CurrentHeader()
	hash := head.Hash()
	number := head.Number.Uint64()
	td := bs.blockchain.GetTd(hash, number)
	return &statusData{
		ProtocolVersion: uint32(1),
		NetworkId:       0,
		TD:              td,
		CurrentBlock:    hash,
		CurrentNumber:   number,
		GenesisBlock:    genesis.Hash(),
	}
}

func checkChainStatus(local *statusData, remote *statusData) error {
	if local.GenesisBlock != remote.GenesisBlock {
		return errResp(ErrGenesisBlockMismatch, "%x (!= %x)", remote.GenesisBlock[:8], local.GenesisBlock[:8])
	}
	if local.NetworkId != remote.NetworkId {
		return errResp(ErrNetworkIdMismatch, "%d (!= %d)", remote.NetworkId, local.NetworkId)
	}
	if local.ProtocolVersion != remote.ProtocolVersion {
		return errResp(ErrProtocolVersionMismatch, "%d (!= %d)", remote.ProtocolVersion, local.ProtocolVersion)
	}
	return nil
}

func (bs *BlockchainStation) handshake(e *router.Event) {
	station := router.NewLocalStation("shake"+e.From.Name(), nil)
	ch := make(chan *router.Event)
	sub := router.Subscribe(station, ch, router.DownloaderStatusMsg, &statusData{})
	defer sub.Unsubscribe()
	defer router.StationUnregister(station)

	router.SendTo(station, e.From, router.DownloaderGetStatus, "")
	disconnect := func() {
		router.SendTo(nil, nil, router.P2pDisconectPeer, e.From)
	}
	timer := time.After(5 * time.Second)
	select {
	case e := <-ch:
		remote := e.Data.(*statusData)
		if err := checkChainStatus(bs.chainStatus(), remote); err != nil {
			disconnect()
			log.Warn(fmt.Sprintln("handshake error:", err))
			return
		}
		log.Info(fmt.Sprintf("new remote station:%x", []byte(e.From.Name())))
		bs.downloader.AddStation(e.From, remote.TD, remote.CurrentNumber, remote.CurrentBlock)
	case <-timer:
		log.Warn("handshake timeout", e.From.Name())
		disconnect()
	}
}

func (bs *BlockchainStation) loop() {
	for {
		e := <-bs.peerCh
		switch e.Typecode {
		case router.P2pNewPeer:
			go bs.handshake(e)
		case router.P2pDelPeer:
			go bs.downloader.DelStation(e.From)
		default:
			go bs.handleMsg(e)
		}
	}
}

// handleMsg is invoked whenever an inbound message is received from a remote
// peer. The remote connection is torn down upon returning any error.
func (bs *BlockchainStation) handleMsg(e *router.Event) error {
	switch e.Typecode {
	case router.DownloaderGetStatus:
		status := bs.chainStatus()
		router.ReplyEvent(e, router.DownloaderStatusMsg, status)

	case router.DownloaderGetBlockHashMsg:
		query := e.Data.(*getBlcokHashByNumber)
		hashes := make([]common.Hash, 0, query.Amount)
		for len(hashes) < int(query.Amount) {
			header := bs.blockchain.GetHeaderByNumber(query.Number)
			if header == nil {
				break
			}
			hashes = append(hashes, header.Hash())
			if query.Reverse {
				if query.Number < query.Skip+1 {
					break
				}
				query.Number -= query.Skip + 1
			} else {
				query.Number += query.Skip + 1
			}
		}
		router.ReplyEvent(e, router.BlockHashMsg, hashes)
	// Block header query, collect the requested headers and reply
	case router.DownloaderGetBlockHeadersMsg:
		// Decode the complex header query
		query := e.Data.(*getBlockHeadersData)
		if query.Origin.Hash != (common.Hash{}) {
			header := bs.blockchain.GetHeaderByHash(query.Origin.Hash)
			if header == nil {
				router.ReplyEvent(e, router.BlockHeadersMsg, []*types.Header{})
				return nil
			}
			query.Origin.Number = header.Number.Uint64()
		}

		// Gather headers until the fetch or network limits is reached
		var (
			headers []*types.Header
		)
		for len(headers) < int(query.Amount) {
			// Retrieve the next header satisfying the query
			origin := bs.blockchain.GetHeaderByNumber(query.Origin.Number)
			if origin == nil {
				break
			}
			headers = append(headers, origin)

			// Advance to the next header of the query
			if query.Reverse {
				// Number based traversal towards the genesis block
				if query.Origin.Number < query.Skip+1 {
					break
				}
				query.Origin.Number -= query.Skip + 1
			} else {
				// Number based traversal towards the leaf block
				query.Origin.Number += query.Skip + 1
			}
		}

		router.ReplyEvent(e, router.BlockHeadersMsg, headers)
		return nil
	case router.DownloaderGetBlockBodiesMsg:
		// Decode the retrieval message
		hashes := e.Data.([]common.Hash)
		// Gather blocks until the fetch or network limits is reached
		var (
			bodies []*types.Body
		)
		for _, hash := range hashes {
			// Retrieve the requested block body, stopping if enough was found
			body := bs.blockchain.GetBody(hash)
			if body == nil {
				break
			}
			bodies = append(bodies, body)
		}
		router.ReplyEvent(e, router.BlockBodiesMsg, bodies)
		return nil
	}
	return nil
}