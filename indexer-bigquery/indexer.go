// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package indexer_bigquery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dfuse-io/bstream"
	"github.com/dfuse-io/bstream/blockstream"
	"github.com/dfuse-io/bstream/forkable"
	"github.com/dfuse-io/dstore"
	"github.com/dfuse-io/search"
	"github.com/dfuse-io/shutter"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// we need to be able to launch it with Start and Stop Block
// we simply a start block

type IndexerBigQuery struct {
	*shutter.Shutter

	grpcListenAddr string

	StartBlockNum uint64
	StopBlockNum  uint64
	shardSize     uint64

	pipeline *Pipeline
	source   bstream.Source

	indexesStore    dstore.Store
	blocksStore     dstore.Store
	blockstreamAddr string
	blockMapper     search.BlockMapper

	dfuseHooksActionName string
	writePath            string
	Verbose              bool

	ready        bool
	shuttingDown *atomic.Bool

	// Head block time, solely used to report drift
	headBlockTimeLock sync.RWMutex
	headBlockTime     time.Time

	libBlockLock sync.RWMutex
	libBlock     *bstream.Block
}

func NewIndexerBigQuery(
	indexesStore dstore.Store,
	blocksStore dstore.Store,
	blockstreamAddr string,
	blockMapper search.BlockMapper,
	writePath string,
	shardSize uint64,
	grpcListenAddr string,

) *IndexerBigQuery {
	indexer := &IndexerBigQuery{
		Shutter:         shutter.New(),
		shuttingDown:    atomic.NewBool(false),
		indexesStore:    indexesStore,
		blocksStore:     blocksStore,
		blockstreamAddr: blockstreamAddr,
		blockMapper:     blockMapper,
		shardSize:       shardSize,
		writePath:       writePath,
		grpcListenAddr:  grpcListenAddr,
	}

	return indexer
}

func (i *IndexerBigQuery) setReady() {
	i.ready = true
}

func (i *IndexerBigQuery) isReady() bool {
	return i.ready
}

func (i *IndexerBigQuery) Bootstrap(startBlockNum uint64) error {
	zlog.Info("bootstrapping from start blocknum", zap.Uint64("indexer_startblocknum", startBlockNum))
	i.StartBlockNum = startBlockNum
	if i.StartBlockNum%i.shardSize != 0 && i.StartBlockNum != 1 {
		return fmt.Errorf("indexer only starts RIGHT BEFORE the index boundaries, did you specify an irreversible block_id with a round number? It says %d", i.StartBlockNum)
	}
	return i.pipeline.Bootstrap(i.StartBlockNum)
}

func (i *IndexerBigQuery) BuildLivePipeline(targetStartBlockNum, fileSourceStartBlockNum uint64, previousIrreversibleID string) {
	pipe := i.newPipeline(i.blockMapper)

	sf := bstream.SourceFromRefFactory(func(startBlockRef bstream.BlockRef, h bstream.Handler) bstream.Source {
		pipe.SetCatchUpMode()

		var handler bstream.Handler
		var jsOptions []bstream.JoiningSourceOption
		var startBlockNum uint64

		firstCall := startBlockRef.ID() == ""
		if firstCall {
			startBlockNum = fileSourceStartBlockNum
			handler = h
		} else {
			startBlockNum = startBlockRef.Num()
			handler = bstream.NewBlockIDGate(startBlockRef.ID(), bstream.GateExclusive, h)
			jsOptions = append(jsOptions, bstream.JoiningSourceTargetBlockID(startBlockRef.ID()))
		}

		liveSourceFactory := bstream.SourceFactory(func(subHandler bstream.Handler) bstream.Source {
			source := blockstream.NewSource(
				context.Background(),
				i.blockstreamAddr,
				250,
				subHandler,
			)

			// We will enable parallel reprocessing of live blocks, disabled to fix RAM usage
			//			source.SetParallelPreproc(pipe.mapper.PreprocessBlock, 8)

			return source
		})

		fileSourceFactory := bstream.SourceFactory(func(subHandler bstream.Handler) bstream.Source {
			fs := bstream.NewFileSource(
				i.blocksStore,
				startBlockNum,
				2,   // always 2 download threads, ever
				nil, //pipe.mapper.PreprocessBlock,
				subHandler,
			)
			if i.Verbose {
				fs.SetLogger(zlog)
			}
			return fs
		})

		protocolFirstBlock := bstream.GetProtocolFirstBlock
		if protocolFirstBlock > 0 {
			jsOptions = append(jsOptions, bstream.JoiningSourceTargetBlockNum(bstream.GetProtocolFirstBlock))
		}
		js := bstream.NewJoiningSource(fileSourceFactory, liveSourceFactory, handler, jsOptions...)

		return js
	})

	options := []forkable.Option{
		forkable.WithFilters(forkable.StepNew | forkable.StepIrreversible),
	}
	if previousIrreversibleID != "" {
		options = append(options, forkable.WithInclusiveLIB(bstream.NewBlockRef(previousIrreversibleID, fileSourceStartBlockNum)))
	}

	gate := forkable.NewIrreversibleBlockNumGate(targetStartBlockNum, bstream.GateInclusive, pipe)

	forkableHandler := forkable.New(gate, options...)

	// note the indexer will listen for the source shutdown signal within the Launch() function
	// hence we do not need to propagate the shutdown signal originating from said source to the indexer. (i.e es.OnTerminating(....))
	es := bstream.NewEternalSource(sf, forkableHandler)

	i.source = es
	i.pipeline = pipe
}

func (i *IndexerBigQuery) BuildBatchPipeline(targetStartBlockNum, fileSourceStartBlockNum uint64, previousIrreversibleID string) {
	pipe := i.newPipeline(i.blockMapper)

	gate := bstream.NewBlockNumGate(targetStartBlockNum, bstream.GateInclusive, pipe)
	gate.MaxHoldOff = 0

	options := []forkable.Option{
		forkable.WithFilters(forkable.StepIrreversible),
	}

	if previousIrreversibleID != "" {
		options = append(options, forkable.WithInclusiveLIB(bstream.NewBlockRef(previousIrreversibleID, fileSourceStartBlockNum)))
	}

	forkableHandler := forkable.New(gate, options...)

	fs := bstream.NewFileSource(
		i.blocksStore,
		fileSourceStartBlockNum,
		2,
		pipe.mapper.PreprocessBlock,
		forkableHandler,
	)
	if i.Verbose {
		fs.SetLogger(zlog)
	}

	// note the indexer will listen for the source shutdown signal within the Launch() function
	// hence we do not need to propagate the shutdown signal originating from said source to the indexer. (i.e fs.OnTerminating(....))
	i.source = fs
	i.pipeline = pipe
	pipe.SetCatchUpMode()
}

func (i *IndexerBigQuery) Launch() {
	i.OnTerminating(func(e error) {
		zlog.Info("shutting down indexer's source") // TODO: triple check that we want to shutdown the source. PART OF A MERGE where intent is not clear.
		i.source.Shutdown(e)
		zlog.Info("shutting down indexer", zap.Error(e))
		i.cleanup()
	})

	i.serveHealthz()
	zlog.Info("launching pipeline")
	i.source.Run()

	if err := i.source.Err(); err != nil {
		if strings.HasSuffix(err.Error(), CompletedError.Error()) { // I'm so sorry, it is wrapped somewhere in bstream
			zlog.Info("Search Indexing completed successfully")
			i.Shutdown(nil)
			return
		}

		zlog.Error("search indexer source terminated with error", zap.Error(err))
	}

	i.Shutdown(i.source.Err())
	return
}

func (i *IndexerBigQuery) cleanup() {
	zlog.Info("cleaning up indexer")
	i.shuttingDown.Store(true)

	zlog.Info("waiting on uploads")
	i.pipeline.WaitOnUploads()

	zlog.Sync()
	zlog.Info("indexer shutdown complete")
}
